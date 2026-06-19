# Enabling Rancher API audit logging (rancher-desktop)

Rancher UI/API audit logging is **off by default**. It must be turned on at the Rancher
server before there is anything to ship. This is a one-time setup step — the operator
(see [../operator](../operator)) only handles *exporting* the logs, not enabling them.

## How it works

When `auditLog.enabled=true`, the Rancher server writes JSON audit entries to
`/var/log/auditlog/rancher-api-audit.log`. With `auditLog.destination=sidecar` (the chart
default), the chart injects a shared `emptyDir` and a **`rancher-audit-log` sidecar** that
tails that file to **stdout**, where Grafana Alloy can scrape it via the Kubernetes API.

Audit levels: `0` metadata only · `1` + request/response headers · `2` + request body ·
`3` + response body. This sandbox uses **level 1**.

## Enable it (Helm-managed Rancher — preferred)

The `rancher` release is Helm-managed, so enable audit logging on the release (survives
upgrades). Pin the chart to the installed version and reuse existing values:

```bash
helm repo add rancher-latest https://releases.rancher.com/server-charts/latest
helm repo update rancher-latest

helm --kube-context rancher-desktop -n cattle-system upgrade rancher \
  rancher-latest/rancher --version 2.13.1 --reuse-values \
  --set auditLog.enabled=true --set auditLog.level=1
```

This triggers a rolling restart of the `rancher` deployment.

## Verify

```bash
# Deployment now has two containers: rancher + rancher-audit-log
kubectl --context rancher-desktop -n cattle-system get deploy rancher \
  -o jsonpath='{.spec.template.spec.containers[*].name}{"\n"}'

# Sidecar emits audit JSON
POD=$(kubectl --context rancher-desktop -n cattle-system get pods -l app=rancher \
  -o jsonpath='{.items[0].metadata.name}')
kubectl --context rancher-desktop -n cattle-system logs "$POD" -c rancher-audit-log --tail=5
```

A line looks like (level 1, headers included, secrets `[redacted]`):

```json
{"auditID":"67a4e412-…","requestURI":"/v3/clusters","user":{"name":"u-abc123","group":["…"]},
 "method":"POST","remoteAddr":"10.42.0.1:33000","responseCode":201,
 "requestTimestamp":"2026-06-19T17:35:02Z","responseTimestamp":"2026-06-19T17:35:02Z",
 "requestHeader":{…},"responseHeader":{…}}
```

Key fields the export pipeline and Grafana dashboard rely on: `user.name` (the human
actor — for external IdPs this is the IdP username), `method` (the HTTP verb — Rancher has
no separate `verb` field), `requestURI` (the resource), `responseCode`, and the timestamps.

## Fallback: Rancher not Helm-managed

If `helm -n cattle-system list` does not show a `rancher` release, patch the Deployment to
add the env vars, a shared `emptyDir` at `/var/log/auditlog`, and the
`rancher-audit-log` sidecar (`busybox`/`rancher` image running
`tail -F /var/log/auditlog/rancher-api-audit.log`). The Helm path is strongly preferred
because a direct patch is reverted on the next Rancher Helm upgrade.
