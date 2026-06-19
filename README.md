# Rancher Audit Log Sandbox

Make **actions taken in the Rancher UI by any user** readable and queryable in Grafana.

A user clicks around the Rancher dashboard → Rancher writes API audit JSON → a Grafana
Alloy shipper (managed by a small operator) tails it and pushes to Loki → you query
"who did what, when" in Grafana.

## Architecture

```
rancher-desktop (lima VM)                         bilbo / k3d-bilbo (Docker Desktop)
┌──────────────────────────────────┐              ┌───────────────────────────────┐
│ cattle-system/rancher             │   push       │ monitoring/loki (single-binary)│
│   rancher  (AUDIT_LEVEL=1)        │   over the   │ monitoring/grafana             │
│   rancher-audit-log sidecar ──────┼──┐ Mac host   │   Loki datasource +            │
│        (audit JSON → stdout)      │  │            │   "Rancher Audit" dashboard    │
│                                   │  │ Alloy      │ exposed via Traefik:           │
│ AuditLogConfig CR (rancheraudit.io)│  │ tails via  │   /loki  ·  grafana.localhost  │
│ operator → Grafana Alloy Deploy ──┼──┘ K8s API,   └───────────────────────────────┘
└──────────────────────────────────┘    labels & pushes → http://192.168.5.2/loki/api/v1/push
```

Two important facts that shaped this design:

- **Rancher UI audit logs are an application-level log**, not Kubernetes `kube-apiserver`
  audit. Rancher writes them to a file streamed to a `rancher-audit-log` sidecar's stdout.
  (An earlier iteration used a Kubernetes `AuditSink` + webhook forwarder — that API was
  removed in Kubernetes 1.19 and never carried Rancher UI events. It has been removed.)
- **Promtail is end-of-life** (March 2026). The shipper is **Grafana Alloy**.

## Layout

- `bilbo/` — Helm values + install script for the Loki/Grafana backend, the Grafana
  dashboard, and the Traefik ingress. See [bilbo/install.sh](bilbo/install.sh).
- `operator/` — Kubebuilder operator (`rancheraudit.io/v1alpha1`, kind `AuditLogConfig`)
  that reconciles a Grafana Alloy shipper from a CR.
- `docs/` — [usage.md](docs/usage.md) (end-to-end run) and
  [enable-rancher-audit.md](docs/enable-rancher-audit.md) (turning on Rancher auditing).

## Quick start

```bash
# 1. Backend on bilbo
./bilbo/install.sh                       # Loki + Grafana + dashboard + ingress

# 2. Turn on Rancher audit logging on rancher-desktop (one-time) — see docs
helm --kube-context rancher-desktop -n cattle-system upgrade rancher \
  rancher-latest/rancher --version 2.13.1 --reuse-values \
  --set auditLog.enabled=true --set auditLog.level=1

# 3. Operator + CR on rancher-desktop
kubectl --context rancher-desktop apply -f operator/config/crd/bases
cd operator && make run            # dev; or `make deploy` for in-cluster
kubectl --context rancher-desktop apply -f config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
```

Open **http://grafana.localhost** (admin/admin) → dashboard **Rancher Audit**, or run
`{job="rancher-audit"} | json | __error__="" | user_name="<user>"` in Explore.

See [docs/usage.md](docs/usage.md) for the full walkthrough and verification steps.
