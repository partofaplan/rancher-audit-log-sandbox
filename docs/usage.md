# Usage

## 1. Deploy Loki and Grafana into `bilbo`

From the repository root:

```bash
cd bilbo/helm
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
helm upgrade --install loki grafana/loki -n monitoring --create-namespace -f loki/values.yaml
helm upgrade --install grafana grafana/grafana -n monitoring -f grafana/values.yaml
```

## 2. Configure Grafana

1. Expose Grafana to access the UI.
2. Add a Loki data source pointing to `http://loki.monitoring.svc.cluster.local:3100`.
3. Import or build dashboards for audit logs.

## 3. Build the operator for `rancher-desktop`

From the repository root:

```bash
cd operator
go mod tidy
go build ./cmd/manager
```

The operator uses an `AuditLogConfig` CRD to create audit forwarding resources in the same namespace.

## 4. Install the operator and CRD

```bash
kubectl config use-context rancher-desktop
kubectl apply -f operator/config/crd/bases
kubectl apply -f operator/config/rbac/role.yaml
kubectl apply -f operator/config/rbac/role_binding.yaml
kubectl apply -f operator/config/manager/manager.yaml
```

## 5. Create a sample audit configuration

```bash
kubectl apply -f operator/config/samples/rancheraudit_v1alpha1_auditlogconfig.yaml
```

## 6. Verify audit ingestion

In Grafana, query Loki for logs labeled with `job="rancher-audit"` and inspect Kubernetes audit fields such as `user`, `verb`, and `objectRef`.
