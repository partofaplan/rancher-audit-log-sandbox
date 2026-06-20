# Testing the operator without ELK

You can fully validate the operator and its shipper before any Elasticsearch/Kibana exists.
There are two things to check; neither needs ELK.

Set these to match your install (chart defaults shown):

```bash
CR=rancher-audit            # AuditLogConfig name
NS=rancher-audit-system     # release namespace
```

## 1. The controller reconciles

Apply an `AuditLogConfig` with a **placeholder** Elasticsearch host — the operator doesn't
connect to ES, it only builds the shipper resources, so a fake host is fine:

```bash
kubectl -n "$NS" apply -f - <<EOF
apiVersion: rancheraudit.io/v1alpha1
kind: AuditLogConfig
metadata: { name: $CR }
spec:
  elasticsearch: { host: http://elasticsearch.invalid:9200, index: rancher-audit }
EOF
```

Check the controller did its job:

```bash
# status flips to Ready / Reconciled
kubectl -n "$NS" get auditlogconfig "$CR" -o jsonpath='{.status.ready} {.status.conditions[0].reason}{"\n"}'

# it created the shipper resources
kubectl -n "$NS" get cm,sa,daemonset -l app.kubernetes.io/instance="$CR"
kubectl get clusterrole,clusterrolebinding | grep "audit-shipper-$NS-$CR"

# inspect the generated Filebeat config it would ship with
kubectl -n "$NS" get cm "audit-shipper-$CR" -o jsonpath='{.data.filebeat\.yml}'

# operator logs
kubectl -n "$NS" logs deploy -l app.kubernetes.io/name=rancher-audit-log-operator
```

Finalizer/cleanup check — delete the CR and confirm the owned resources and the cluster RBAC
go away:

```bash
kubectl -n "$NS" delete auditlogconfig "$CR"
kubectl -n "$NS" get daemonset -l app.kubernetes.io/instance="$CR"   # gone
kubectl get clusterrole | grep "audit-shipper-$NS-$CR"               # gone
```

## 2. The shipper reads & parses the Rancher audit log

First confirm the **source** is producing audit JSON (requires Rancher audit logging enabled —
see [enable-rancher-audit.md](enable-rancher-audit.md)):

```bash
POD=$(kubectl -n cattle-system get pods -l app=rancher -o jsonpath='{.items[0].metadata.name}')
kubectl -n cattle-system logs "$POD" -c rancher-audit-log --tail=3   # JSON lines
```

Then confirm Filebeat is harvesting (even pointed at the fake ES, the *read* side works; only
the output fails, which is expected):

```bash
kubectl -n "$NS" logs ds/audit-shipper-$CR | grep "Harvester started"   # finds the audit container
```

### See the enriched events (no Elasticsearch) — console output

To actually see the parsed `audit.*` events — including the `audit.summary` sentences — run
the shipper's **exact** generated pipeline but with a console output instead of ES:

```bash
operator/hack/console-test.sh "$CR" "$NS"      # runs a throwaway pod, tails its logs
```

You'll see events like:

```json
{ "audit": { "actor": "zperkins", "verb": "delete", "resource": "pods",
             "target": "default/web-77f9", "category": "workload",
             "summary": "zperkins delete pods default/web-77f9" }, ... }
```

That proves harvest → JSON decode → enrichment (actor / verb / resource / category / summary)
all work — without any Elasticsearch. Clean up when done:

```bash
operator/hack/console-test.sh --cleanup "$CR" "$NS"
```

(The script derives the console config from the operator-generated ConfigMap, so it tests the
real pipeline. `unknown`/empty entries are the occasional oversized log line split by the
container runtime — harmless; they're filtered out at query time in Kibana.)
