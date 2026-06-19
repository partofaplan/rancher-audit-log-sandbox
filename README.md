# Rancher Audit Log Sandbox

Make **actions taken in the Rancher UI by any user** readable and queryable in Kibana.

A user clicks around the Rancher dashboard → Rancher writes API audit JSON → a Filebeat
shipper (managed by a small operator) tails it and ships to Elasticsearch → you explore
"who did what, when" in Kibana as readable sentences.

## Architecture

```
rancher-desktop (lima VM)                         bilbo / k3d-bilbo (Docker Desktop)
┌──────────────────────────────────┐              ┌───────────────────────────────┐
│ cattle-system/rancher             │   ship       │ monitoring/elasticsearch       │
│   rancher  (AUDIT_LEVEL=1)        │   over the   │   (single node, security off)  │
│   rancher-audit-log sidecar ──────┼──┐ Mac host   │ monitoring/kibana              │
│        (audit JSON → stdout)      │  │            │   data view + saved search     │
│                                   │  │ Filebeat   │ exposed via Traefik:           │
│ AuditLogConfig CR (rancheraudit.io)│  │ tails node │   /es  ·  kibana.localhost     │
│ operator → Filebeat DaemonSet ────┼──┘ logs,      └───────────────────────────────┘
└──────────────────────────────────┘    parses JSON, builds the sentence, ships →
                                         http://192.168.5.2:80/es  (index: rancher-audit)
```

Each audit entry is enriched at ingest into `audit.*` fields, including an
`audit.summary` sentence like **`zperkins create deployments monitoring/web`**:

- `audit.actor` — the Rancher local/login username (`user.extra.username`), falling back
  to the principal (`user.name`), then `unknown`.
- `audit.verb` — mapped from the HTTP method (POST→create, DELETE→delete, PUT/PATCH→
  update, GET→get; POST with `?action=`→invoke).
- `audit.resource` / `audit.target` — the kind and namespace/name parsed from `requestURI`.

Two facts that shaped this design:

- **Rancher UI audit logs are an application-level log**, not Kubernetes `kube-apiserver`
  audit. Rancher writes them to a file streamed to a `rancher-audit-log` sidecar's stdout.
- Because the rancher-desktop node runs **dockerd**, container logs live under
  `/var/lib/docker/containers` (the `/var/log/pods/.../0.log` files symlink there), so the
  Filebeat DaemonSet mounts that path too.

> This repo previously used Loki + Grafana + Grafana Alloy; it was refactored to the ELK
> stack (Elasticsearch + Kibana + Filebeat). See git history for the Loki/Grafana version.

## Layout

- `bilbo/` — manifests + install script for the ELK backend.
  - `bilbo/elk/{elasticsearch,kibana,ingress}.yaml`, `bilbo/elk/setup-kibana.sh`,
    [bilbo/install.sh](bilbo/install.sh).
- `operator/` — Kubebuilder operator (`rancheraudit.io/v1alpha1`, kind `AuditLogConfig`)
  that reconciles a Filebeat shipper from a CR.
- `docs/` — [usage.md](docs/usage.md) (end-to-end run) and
  [enable-rancher-audit.md](docs/enable-rancher-audit.md) (turning on Rancher auditing).

## Quick start

```bash
# 1. ELK backend on bilbo (Elasticsearch + Kibana + data view/saved search)
./bilbo/install.sh
echo "127.0.0.1  kibana.localhost" | sudo tee -a /etc/hosts   # for the Kibana UI

# 2. Turn on Rancher audit logging on rancher-desktop (one-time) — see docs
helm --kube-context rancher-desktop -n cattle-system upgrade rancher \
  rancher-latest/rancher --version 2.13.1 --reuse-values \
  --set auditLog.enabled=true --set auditLog.level=1

# 3. Operator + CR on rancher-desktop
cd operator
./build-image.sh                                          # build the operator image (amd64)
kubectl --context rancher-desktop apply -k config/default # CRD + RBAC + operator Deployment
kubectl --context rancher-desktop apply -f config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
# (for development you can skip the image and run the controller locally with `make run`)
```

## Operator image & deploying to another Rancher

The operator ships as a small container image (~17 MB, `scratch` + a static, cross-compiled
binary). [operator/build-image.sh](operator/build-image.sh) cross-compiles offline and builds
per-platform:

```bash
cd operator
IMG=rancher-audit-log-operator:0.1.0 PLATFORM=linux/amd64 ./build-image.sh   # amd64 (default)
# multi-arch + push to your registry:
IMG=myregistry.example.com/rancher-audit-log-operator:0.1.0 \
  PLATFORM=linux/amd64,linux/arm64 PUSH=1 ./build-image.sh
```

Deploy to **any** Rancher cluster (the operator watches all namespaces):

```bash
cd operator/config/manager && kustomize edit set image controller=<your-image>   # or edit kustomization.yaml
kubectl apply -k operator/config/default
```

Then point an `AuditLogConfig` at your **existing ELK** — host, basic auth, and TLS (private
CA or skip-verify) are all supported. See
[operator/config/samples/external-elasticsearch.yaml](operator/config/samples/external-elasticsearch.yaml):

```yaml
spec:
  elasticsearch:
    host: https://elasticsearch.example.com:9200
    index: rancher-audit
    basicAuthSecretRef: elastic-credentials   # Secret with username/password
    tls:
      caSecretRef: elastic-ca                 # Secret with ca.crt (or insecureSkipVerify: true)
```

(Audit logging must be enabled on that cluster's Rancher first — step 2 / [docs/enable-rancher-audit.md](docs/enable-rancher-audit.md).)

## Installing the dashboard in another ELK / Kibana

The dashboard, visualizations, data view, and saved searches are a portable Kibana
**saved-objects bundle**: [bilbo/elk/kibana-objects.ndjson](bilbo/elk/kibana-objects.ndjson).
You can load it into any Kibana 8.x — it's independent of where the operator runs.

**Prerequisite:** the bundle's data view is titled **`rancher-audit`**, so it must match the
index your shipper writes to (`spec.elasticsearch.index`, default `rancher-audit`). Use the
same index name, or after importing rename the data view (Stack Management → Data Views) — the
saved searches and dashboard reference it by id, so they follow automatically.

### Option A — script (against any Kibana)

[bilbo/elk/setup-kibana.sh](bilbo/elk/setup-kibana.sh) POSTs the bundle to Kibana's
`saved_objects/_import` API (idempotent, `overwrite=true`). Point it at your Kibana, drop the
local vhost header, and pass credentials — basic auth or an API key (create one under Kibana →
Stack Management → API keys):

```bash
# basic auth
KIBANA_URL=https://kibana.example.com KIBANA_HOST= \
  KIBANA_USER=elastic KIBANA_PASS='…' ./bilbo/elk/setup-kibana.sh

# API key
KIBANA_URL=https://kibana.example.com KIBANA_HOST= \
  KIBANA_APIKEY='<base64 id:api_key>' ./bilbo/elk/setup-kibana.sh
```

(`KIBANA_HOST=` clears the `kibana.localhost` Host header used only for the local Traefik
setup. If Kibana is served under a base path, include it in `KIBANA_URL`,
e.g. `https://host/kibana`.)

### Option B — Kibana UI

Stack Management → **Saved Objects** → **Import** → choose `kibana-objects.ndjson` →
enable "Automatically overwrite conflicts" → Import.

Either way, then open **Dashboards → Rancher Audit Overview**. To re-export after editing it in
the UI: Stack Management → Saved Objects → select the objects → Export (keep "include related"),
and replace `bilbo/elk/kibana-objects.ndjson`.

Open **http://kibana.localhost** → Dashboards → **Rancher Audit Overview** (events over time,
breakdown by category, top actions, top users, translated event sentences, and raw log
output), or query Elasticsearch directly: `curl http://localhost/es/rancher-audit/_search`.

See [docs/usage.md](docs/usage.md) for the full walkthrough and verification steps.
