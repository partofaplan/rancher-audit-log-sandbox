# Rancher Audit Log Sandbox

This repository contains an implementation plan and starter code to deploy a centralized Grafana/Loki stack into the `bilbo` cluster and a Kubernetes operator for `rancher-desktop` that configures cluster audit export.

## Goal
- Deploy Loki and Grafana into `bilbo`
- Build a Go/KubeBuilder-style operator for `rancher-desktop`
- Define an `AuditLogConfig` CRD to manage audit export
- Export audit events from Rancher/Kubernetes into Loki for Grafana dashboards

## Structure
- `bilbo/helm/` — Helm values and deployment guidance for Grafana and Loki
- `operator/` — Go operator code, CRD manifests, sample CRs, and RBAC
- `docs/` — usage and deployment instructions

## Next steps
1. Install the `bilbo` stack using the manifests in `bilbo/helm/`
2. Build and install the operator in `rancher-desktop`
3. Apply the sample `AuditLogConfig` and verify audit events in Grafana
