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
kubectl --context rancher-desktop apply -f operator/config/crd/bases
cd operator && make run            # dev; or `make deploy` for in-cluster
kubectl --context rancher-desktop apply -f config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
```

Open **http://kibana.localhost** → Discover → saved search **Rancher Audit Events**, or query
Elasticsearch directly: `curl http://localhost/es/rancher-audit/_search`.

See [docs/usage.md](docs/usage.md) for the full walkthrough and verification steps.
