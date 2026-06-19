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

## 3. Install the CRD and run the operator

```bash
kubectl --context rancher-desktop apply -f operator/config/crd/bases

cd operator
make run          # runs the controller locally against the current kube-context (dev)
# — or build an image and run it in-cluster: make docker-build docker-push deploy IMG=...
```

`make run`/codegen use vendored `controller-gen`/`kustomize` in `operator/bin` + the module
cache, so they work offline.

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
→ Discover → saved search **Rancher Audit Events**. Each row shows the enriched fields, with
`audit.summary` reading like a sentence — e.g. `zperkins create deployments monitoring/web`,
`admin update ingresses monitoring/grafana`, `admin delete pods default/foo`.

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

## Teardown

```bash
kubectl --context rancher-desktop delete -f operator/config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
# the operator removes the Filebeat DaemonSet/ConfigMap/SA (owner refs) and the
# ClusterRole/ClusterRoleBinding (finalizer).
kubectl --context k3d-bilbo -n monitoring delete -f bilbo/elk/
```
