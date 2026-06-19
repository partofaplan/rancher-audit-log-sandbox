# Usage

End-to-end: deploy the Elasticsearch/Kibana (ELK) backend on **bilbo**, enable Rancher
audit logging on **rancher-desktop**, run the operator, and view per-user Rancher UI
actions in Kibana.

Contexts: `k3d-bilbo` (backend) and `rancher-desktop` (Rancher + operator).

## 1. Deploy Elasticsearch + Kibana on bilbo

```bash
./bilbo/install.sh
```

This applies single-node Elasticsearch (security **disabled** — plain HTTP, sandbox only),
Kibana, Traefik ingress, and a Kibana data view + saved search. It creates:

- `http://kibana.localhost` — Kibana UI (no login; ES security is off)
- `http://localhost/es/...` — Elasticsearch API, fronted by Traefik under a stripped `/es`
  prefix and published on the Mac host so the shipper in the other cluster can reach it.

> **Local hosts entry:** the clusters run locally on this Mac and k3d publishes bilbo's
> Traefik on the host's `:80`. Kibana uses the hostname `kibana.localhost`, so add:
> ```
> echo "127.0.0.1  kibana.localhost" | sudo tee -a /etc/hosts
> ```
> The ES path (`/es`) is reached by IP and needs no hosts entry.

Verify:

```bash
kubectl --context k3d-bilbo -n monitoring get pods
curl -s http://localhost/es/_cluster/health        # status green
curl -s -H "Host: kibana.localhost" http://localhost/api/status | jq .status.overall.level
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

## 3. Build the operator image and deploy it

The operator is a small container image — a static, cross-compiled binary on `scratch`
(~17 MB). `build-image.sh` cross-compiles offline (from the Go module cache) and builds per
platform, so an **amd64** image builds fine even on an arm64 Mac with no qemu:

```bash
cd operator
IMG=rancher-audit-log-operator:0.1.0 PLATFORM=linux/amd64 ./build-image.sh
# the image is also saved to operator/dist/<name>-amd64.tar (docker load / push elsewhere)
```

Deploy it (CRD + RBAC + operator Deployment, namespace `rancher-audit-log-operator-system`):

```bash
# config/manager/kustomization.yaml already pins image -> rancher-audit-log-operator:0.1.0
kubectl --context rancher-desktop apply -k config/default
kubectl --context rancher-desktop -n rancher-audit-log-operator-system get pods
```

> For the **local** arm64 clusters, build the arm64 variant into the cluster's docker so it's
> available without a registry:
> `IMG=rancher-audit-log-operator:0.1.0 PLATFORM=linux/arm64 DOCKER_CONTEXT=rancher-desktop ./build-image.sh`
>
> For **another Rancher**, push to a registry and set the image:
> `IMG=myreg/op:0.1.0 PLATFORM=linux/amd64,linux/arm64 PUSH=1 ./build-image.sh`, then
> `cd config/manager && kustomize edit set image controller=myreg/op:0.1.0` before applying.

For quick development without an image, run the controller locally instead:
`kubectl apply -f config/crd/bases && make run` (codegen/kustomize tooling in `operator/bin`
+ the module cache work offline).

## 4. Apply an AuditLogConfig

```bash
kubectl --context rancher-desktop apply -f operator/config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
kubectl --context rancher-desktop get auditlogconfig            # READY should be true
```

The operator reconciles a Filebeat DaemonSet (+ConfigMap +ServiceAccount +ClusterRole/Binding):

```bash
kubectl --context rancher-desktop -n default get ds,cm,sa -l app.kubernetes.io/instance=auditlogconfig-sample
kubectl --context rancher-desktop -n default logs ds/audit-shipper-auditlogconfig-sample | grep "Harvester started"
```

Key spec fields (`operator/config/samples/...yaml`):

| field | meaning | default |
|-------|---------|---------|
| `spec.elasticsearch.host` | ES endpoint reachable from the shipper, **with port** | — (e.g. `http://192.168.5.2:80`) |
| `spec.elasticsearch.pathPrefix` | base path when ES is behind a proxy | unset (use `/es` for bilbo) |
| `spec.elasticsearch.index` | target index | `rancher-audit` |
| `spec.elasticsearch.basicAuthSecretRef` | Secret (keys `username`/`password`) | unset |
| `spec.elasticsearch.tls.caSecretRef` / `.insecureSkipVerify` | trust a private CA (Secret with `ca.crt`) / skip verification — for an existing HTTPS ELK | unset (public CA needs neither) |
| `spec.source.namespace` / `podSelector` / `container` | what to tail | `cattle-system` / `app=rancher` / `rancher-audit-log` |
| `spec.filebeat.image` | shipper image | `docker.elastic.co/beats/filebeat:8.17.3` |

> **Gotchas (both verified the hard way):**
> - Put the **port** in `host` — Filebeat defaults to `:9200`, but bilbo's Traefik is on `:80`.
> - The rancher-desktop node runs **dockerd**, so `/var/log/pods/.../0.log` symlinks into
>   `/var/lib/docker/containers`; the DaemonSet mounts that path so Filebeat can follow them.
> - Cross-VM: from a rancher-desktop pod, bilbo is reached via the Mac host gateway
>   `192.168.5.2` (or `192.168.64.1`). Probe with:
>   `kubectl --context rancher-desktop run probe --rm -it --image=curlimages/curl -- sh -c 'curl -s -o /dev/null -w "%{http_code}\n" http://192.168.5.2:80/es/_cluster/health'`

## 5. Verify in Kibana

Generate activity by clicking around the Rancher UI, then open **http://kibana.localhost**
→ Dashboards → **Rancher Audit Overview**. The dashboard (imported by `setup-kibana.sh` from
`bilbo/elk/kibana-objects.ndjson`) has:

| Panel | What it shows |
|-------|---------------|
| Audit events over time | volume by action (stacked date histogram) |
| Events by category | `audit.category` donut — workload / rbac / networking / config / auth / cluster / catalog / system |
| Events by action | `audit.verb` donut — create / update / delete / get / invoke |
| Top users | `audit.actor` (local login name) by event count |
| Top events | `audit.event` (verb + resource) by count |
| Rancher Audit Events | **translated sentences** — `audit.summary` per event, e.g. `zperkins delete pods default/web` |
| Rancher Audit Raw Log | the raw audit JSON (`message` + key `rancher.*` fields) |

`audit.summary` reads like a sentence — e.g. `zperkins create deployments monitoring/web`,
`admin update ingresses monitoring/grafana`, `admin delete pods default/foo`. The standalone
saved searches (**Rancher Audit Events**, **Rancher Audit Raw Log**) are also available under
Discover.

Filter with KQL, e.g. only changes by a user:

```
audit.actor : "zperkins" and audit.verb : ("create" or "update" or "delete")
```

Or query Elasticsearch directly:

```bash
# distinct actors
curl -s http://localhost/es/rancher-audit/_search -H 'Content-Type: application/json' \
  -d '{"size":0,"aggs":{"actors":{"terms":{"field":"audit.actor.keyword","size":20}}}}'

# recent mutating actions by real users
curl -s http://localhost/es/rancher-audit/_search -H 'Content-Type: application/json' -d '{
  "query":{"bool":{"must_not":[{"term":{"audit.actor.keyword":"system:cattle:error"}}],
    "filter":[{"terms":{"audit.verb.keyword":["create","update","delete","invoke"]}}]}},
  "sort":[{"@timestamp":"desc"}], "_source":["audit.summary"]}'
```

The decoded Rancher JSON is kept under `rancher.*` (e.g. `rancher.requestURI`,
`rancher.user.name`, `rancher.responseCode`) for full detail alongside the `audit.*` summary.

To load this same dashboard into a **different** Kibana (e.g. an existing ELK), import
`bilbo/elk/kibana-objects.ndjson` there — via `setup-kibana.sh` (with `KIBANA_URL` + auth) or
the Kibana UI. See the README's "Installing the dashboard in another ELK / Kibana".

## Teardown

```bash
kubectl --context rancher-desktop delete -f operator/config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
# the operator removes the Filebeat DaemonSet/ConfigMap/SA (owner refs) and the
# ClusterRole/ClusterRoleBinding (finalizer).
kubectl --context k3d-bilbo -n monitoring delete -f bilbo/elk/
```
