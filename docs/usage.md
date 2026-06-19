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
→ dashboard **Rancher Audit**, or in Explore run LogQL:

```logql
# every UI action, reformatted for reading
{job="rancher-audit"} | json | __error__="" | line_format "{{.user_name}}  {{.method}}  {{.requestURI}}  -> {{.responseCode}}"

# actions by a specific human user
sum by (requestURI) (count_over_time({job="rancher-audit"} | json | __error__="" | user_name="user-xdzqk" [1h]))
```

The audit JSON exposes `user.name` (the human actor — for external IdPs, the IdP username),
`method` (Rancher has no separate `verb`), `requestURI`, `responseCode`, and timestamps.

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
