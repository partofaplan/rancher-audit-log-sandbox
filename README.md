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
                                         http://192.168.64.1:9200  (ES NodePort; index: rancher-audit)
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

This repo serves two purposes: a **local sandbox** (stand the whole thing up on two k3d/
Rancher-Desktop clusters to develop and demo — the diagram above), and a **production
deployment** into an air-gapped Rancher cluster that ships to an Elasticsearch run by another
team. The production path is the Helm chart + the two handoff packages below.

## Layout

- `operator/` — Kubebuilder operator (`rancheraudit.io/v1alpha1`, kind `AuditLogConfig`) that
  reconciles a Filebeat shipper from a CR. Image built by
  [operator/build-image.sh](operator/build-image.sh).
- `charts/rancher-audit-log-operator/` — **Helm chart** to deploy the operator (registry
  prefix, ELK endpoint/auth/TLS, AuditLogConfig). The production deploy unit.
- `elk-integration/` — **handoff package for the ELK team**: index template, shipper role,
  the Kibana dashboard bundle, and [ELK-INTEGRATION.md](elk-integration/ELK-INTEGRATION.md).
- `docs/` — [usage.md](docs/usage.md) (sandbox walkthrough),
  [air-gap.md](docs/air-gap.md) (mirror images + chart install),
  [enable-rancher-audit.md](docs/enable-rancher-audit.md) (turn on Rancher auditing),
  [testing-without-elk.md](docs/testing-without-elk.md) (verify the operator before ELK exists).
- `scripts/build-airgap-bundle.sh` + `.github/workflows/airgap-bundle.yml` — build a single
  downloadable air-gap tarball (images + chart + handoff + docs), attached to GitHub Releases.
- `bilbo/` — **local sandbox only**: a throwaway Elasticsearch+Kibana on k3d plus an install
  script. Not used in production (the real ELK is external).

## Quick start (local sandbox)

```bash
# 1. Sandbox ELK on bilbo (Elasticsearch + Kibana + index template + dashboard)
./bilbo/install.sh
echo "127.0.0.1  kibana.localhost" | sudo tee -a /etc/hosts   # for the Kibana UI

# 2. Turn on Rancher audit logging on rancher-desktop (one-time) — see docs
helm --kube-context rancher-desktop -n cattle-system upgrade rancher \
  rancher-latest/rancher --version 2.13.1 --reuse-values \
  --set auditLog.enabled=true --set auditLog.level=1

# 3. Build the operator image, then install the chart pointed at the sandbox ES
cd operator && ./build-image.sh \
  && IMG=rancher-audit-log-operator:0.1.0 PLATFORM=linux/arm64 DOCKER_CONTEXT=rancher-desktop ./build-image.sh
helm --kube-context rancher-desktop install rancher-audit charts/rancher-audit-log-operator \
  -n rancher-audit-system --create-namespace \
  --set elasticsearch.host=http://192.168.64.1:9200   # ES NodePort on the Mac host (see docs/usage.md)
# (for quick controller-only dev: kubectl apply -f operator/config/crd/bases && cd operator && make run)
```

## Production: air-gapped Rancher + external ELK

In production this splits across **two teams**, with a small contract between them:

```
  YOUR side (Rancher / operator)                    ELK TEAM's side (Elasticsearch + Kibana)
  ┌─────────────────────────────────┐               ┌──────────────────────────────────────┐
  │ air-gapped Rancher cluster      │   ships to     │ index template (audit.* mappings)      │
  │ Helm chart → operator → Filebeat├──────────────▶ │ shipper role / API key                 │
  │ images from internal registry   │  contract:     │ "Rancher Audit Overview" dashboard     │
  └─────────────────────────────────┘  endpoint,     └──────────────────────────────────────┘
                                        index, creds, CA, network egress
```

### Your side — deploy the operator (air-gapped)

**Easiest:** download the prebuilt bundle from the GitHub Release
(`rancher-audit-log-airgap-<version>.tar.gz`, produced by the
[air-gap-bundle workflow](.github/workflows/airgap-bundle.yml)) — it contains both images, the
chart, the ELK handoff, and `load-images.sh`/`install` scripts. Carry it in, run
`./load-images.sh <registry>`, edit `values-example.yaml`, `helm install`. See
[docs/air-gap.md](docs/air-gap.md).

Or do it by hand: the operator is a ~17 MB `scratch` image (static, cross-compiled binary), so
it's just load-and-push. Mirror **two** images into your internal registry — the operator and
the Filebeat shipper — then install the chart with a single registry prefix. In short:

```bash
cd operator && IMG=rancher-audit-log-operator:0.1.0 PLATFORM=linux/amd64 ./build-image.sh
# mirror operator/dist/...amd64.tar and docker.elastic.co/beats/filebeat:8.17.3 into <REG>, then:
helm install rancher-audit charts/rancher-audit-log-operator -n rancher-audit-system --create-namespace \
  --set image.registry=<REG> --set imagePullSecrets[0].name=internal-registry \
  --set elasticsearch.host=https://es.corp.example:9200 \
  --set elasticsearch.auth.existingSecret=es-creds \
  --set elasticsearch.tls.existingCASecret=es-ca
```

`image.registry` prefixes **both** images. The ES endpoint, credentials, and CA come from the
ELK team (below). Audit logging must be enabled on that cluster's Rancher first
([docs/enable-rancher-audit.md](docs/enable-rancher-audit.md)). See
[charts/rancher-audit-log-operator/values.yaml](charts/rancher-audit-log-operator/values.yaml)
for every option (basic auth, inline creds, `insecureSkipVerify`, source pod selector, …).

### ELK team's side — onboard the data + dashboard

Hand the ELK team the [elk-integration/](elk-integration/) folder — it's self-contained.
[ELK-INTEGRATION.md](elk-integration/ELK-INTEGRATION.md) walks them through: allow the network
path, apply the **index template** (`index-template.json` → `audit.*` mapped as keyword/date),
create a least-privilege **shipper credential** (`shipper-role.json` → API key/user, returned
to your side), and import the **dashboard** (`rancher-audit-dashboard.ndjson`) — events over
time, breakdown by category, top actions/users, translated event sentences, and raw log output.

> The dashboard's data view is titled **`rancher-audit`** and must match the index the shipper
> writes to (`elasticsearch.index`, default `rancher-audit`). The index template and dashboard
> are a matched pair — apply the template before data flows so `audit.*` are keyword/date.

See [docs/usage.md](docs/usage.md) for the full sandbox walkthrough and verification steps.
