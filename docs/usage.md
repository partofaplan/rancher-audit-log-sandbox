# Usage

End-to-end: deploy the Loki/Grafana backend on **bilbo**, enable Rancher audit logging on
**rancher-desktop**, run the operator, and view per-user Rancher UI actions in Grafana.

Contexts used: `k3d-bilbo` (backend) and `rancher-desktop` (Rancher + operator).

## 1. Deploy Loki + Grafana on bilbo

```bash
./bilbo/install.sh
```

This installs Loki (single-binary, filesystem) and Grafana into the `monitoring` namespace,
applies the **Rancher Audit** dashboard (ConfigMap picked up by the Grafana dashboards
sidecar), and creates Traefik ingresses:

- `http://grafana.localhost` — Grafana UI (admin/admin)
- `http://localhost/loki/...` — Loki API, published on the Mac host so the shipper in the
  other cluster can push to it.

> **Local hosts entry:** the clusters run locally on this Mac, and k3d (Docker Desktop)
> publishes bilbo's Traefik on the host's `:80`. The Grafana ingress uses the hostname
> `grafana.localhost`, so it needs a hosts entry (macOS does not reliably resolve
> `*.localhost` for non-browser clients):
> ```
> echo "127.0.0.1  grafana.localhost" | sudo tee -a /etc/hosts
> ```
> The Loki push path is IP/`localhost`-based and needs no hosts entry. The Rancher UI is
> reached via its own existing entry (`rancher.k8s.local`).

Verify:

```bash
kubectl --context k3d-bilbo -n monitoring get pods
curl -s http://localhost/loki/api/v1/labels        # 200, Loki reachable on the host
```

## 2. Enable Rancher audit logging (one-time)

The operator only *exports* logs; auditing is enabled on the Rancher server itself. See
[enable-rancher-audit.md](enable-rancher-audit.md). Short version:

```bash
helm repo add rancher-latest https://releases.rancher.com/server-charts/latest
helm --kube-context rancher-desktop -n cattle-system upgrade rancher \
  rancher-latest/rancher --version 2.13.1 --reuse-values \
  --set auditLog.enabled=true --set auditLog.level=1
```

Verify the `rancher-audit-log` sidecar emits JSON:

```bash
POD=$(kubectl --context rancher-desktop -n cattle-system get pods -l app=rancher -o jsonpath='{.items[0].metadata.name}')
kubectl --context rancher-desktop -n cattle-system logs "$POD" -c rancher-audit-log --tail=3
```

## 3. Install the CRD and run the operator

```bash
kubectl --context rancher-desktop apply -f operator/config/crd/bases

cd operator
make run          # runs the controller locally against the current kube-context (dev)
# — or — build an image and run it in-cluster:
# make docker-build docker-push IMG=<registry>/rancher-audit-log-operator:dev
# make deploy IMG=<registry>/rancher-audit-log-operator:dev
```

`make run` needs `controller-gen`/`kustomize`; they are vendored into `operator/bin` and the
module cache, so codegen works offline.

## 4. Apply an AuditLogConfig

```bash
kubectl --context rancher-desktop apply -f operator/config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
kubectl --context rancher-desktop get auditlogconfig          # READY should be true
```

The operator reconciles, in response, a Grafana Alloy shipper:

```bash
kubectl --context rancher-desktop -n default get deploy,cm,sa -l app.kubernetes.io/instance=auditlogconfig-sample
kubectl --context rancher-desktop -n default logs deploy/audit-shipper-auditlogconfig-sample | grep "tailer running"
```

Key spec fields (`operator/config/samples/...yaml`):

| field | meaning | default |
|-------|---------|---------|
| `spec.loki.url` | Loki push endpoint | — (e.g. `http://192.168.5.2/loki/api/v1/push`) |
| `spec.loki.tenant` | `X-Scope-OrgID` for multi-tenant Loki | unset |
| `spec.loki.basicAuthSecretRef` | Secret (keys `username`/`password`) | unset |
| `spec.loki.externalLabels` | static stream labels | unset |
| `spec.source.namespace` / `podSelector` / `container` | what to tail | `cattle-system` / `app=rancher` / `rancher-audit-log` |
| `spec.alloy.image` | shipper image | `grafana/alloy:v1.17.0` |

> **Networking note:** rancher-desktop and bilbo run in separate local VMs. The shipper
> reaches bilbo's Loki via the Mac host gateway — `192.168.5.2` (verified) or `192.168.64.1`.
> If neither resolves on your machine, probe from a pod:
> `kubectl --context rancher-desktop run probe --rm -it --image=curlimages/curl -- sh -c 'curl -s -o /dev/null -w "%{http_code}\n" http://192.168.5.2/loki/api/v1/labels'`

## 5. Verify in Grafana

Generate activity by clicking around the Rancher UI, then open **http://grafana.localhost**
→ dashboard **Rancher Audit**. The "Audit events" panel renders each entry as a readable
sentence — e.g. `zperkins get pods default`, `admin update ingresses monitoring/grafana`,
`admin create users`.

Use the **Actions** dropdown at the top to switch between **All** activity and **Changes
(create/update/delete)** — the latter filters to mutating HTTP methods
(`POST|PUT|PATCH|DELETE`) and applies to all three panels. It's implemented as a custom
dashboard variable interpolated into the stream selector as `method=~"$actions"`.

### The `actor` (local username) and the event sentence

The Alloy pipeline promotes an **`actor`** stream label: the Rancher **local/login username**
from `user.extra.username` (e.g. `zperkins`, `admin`), falling back to the principal
`user.name` (e.g. `system:cattle:error`) and then `unknown`. So you can group/filter by the
human login name directly:

```logql
# top users by login name
topk(10, sum by (actor) (count_over_time({job="rancher-audit", actor!=""}[1h])))

# everything a specific user did
{job="rancher-audit", actor="zperkins"}
```

The sentence is built at query time: `actor` (label) + a verb mapped from the HTTP `method`
(POST→create, DELETE→delete, PUT/PATCH→update, GET→get) + the resource kind and
namespace/name parsed from `requestURI`. The full LogQL is in the dashboard's logs panel
(`bilbo/dashboard-rancher-audit.yaml`); the core of it:

```logql
{job="rancher-audit"} | json uri="requestURI" | __error__=""
  | uri!~`/dashboard/.*|.*\.(js|css|svg|png|woff2?|ico|map|json)|/healthz|/ping|/v3/connect`
  | label_format verb=`{{ if eq .method "POST" }}create{{ else if eq .method "DELETE" }}delete{{ else if eq .method "PUT" }}update{{ else if eq .method "PATCH" }}update{{ else if eq .method "GET" }}get{{ else }}{{ lower .method }}{{ end }}`
  | label_format path=`{{ regexReplaceAll "\\?.*" .uri "" }}`
  | label_format rtype=`{{ regexReplaceAll "^.*/v[0-9]+(?:alpha[0-9]+|beta[0-9]+)?(?:-public)?/([^/]+).*$" .path "${1}" }}`
  | label_format kind=`{{ regexReplaceAll "^.*\\." .rtype "" }}`
  | label_format target=`{{ regexReplaceAll "^.*/v[0-9]+(?:alpha[0-9]+|beta[0-9]+)?(?:-public)?/[^/]+/?" .path "" }}`
  | line_format `{{ .actor }} {{ .verb }} {{ .kind }}{{ if .target }} {{ .target }}{{ end }}`
```

The audit JSON exposes `user.extra.username` (local/login name), `user.extra.principalid`
(`local://...` marks a local user), `user.name` (the internal principal id), `method`
(Rancher has no separate `verb`), `requestURI`, `responseCode`, and timestamps.

> At `AUDIT_LEVEL=1` some entries include large headers and exceed the container runtime's
> ~16 KB log-line limit, producing split partial lines. The `| __error__=""` filter skips
> those unparseable fragments — that is why every parsing query above includes it.

## Teardown

```bash
kubectl --context rancher-desktop delete -f operator/config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
# the operator removes the Alloy Deployment/ConfigMap/SA (owner refs) and the
# Role/RoleBinding in cattle-system (finalizer).
helm --kube-context k3d-bilbo -n monitoring uninstall grafana loki
```
