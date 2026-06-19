#!/usr/bin/env bash
# Deploy the Loki + Grafana logging backend into the bilbo cluster.
#
#   ./bilbo/install.sh
#
# Idempotent: re-run to apply changes. Targets the k3d-bilbo kube context.
set -euo pipefail

CONTEXT="${KUBE_CONTEXT:-k3d-bilbo}"
NS="${NAMESPACE:-monitoring}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo ">> Using context=${CONTEXT} namespace=${NS}"

helm repo add grafana https://grafana.github.io/helm-charts >/dev/null 2>&1 || true
helm repo update grafana >/dev/null

kubectl --context "${CONTEXT}" create namespace "${NS}" \
  --dry-run=client -o yaml | kubectl --context "${CONTEXT}" apply -f -

echo ">> Installing Loki (single-binary)"
helm --kube-context "${CONTEXT}" upgrade --install loki grafana/loki \
  --version 7.0.0 -n "${NS}" -f "${HERE}/helm/loki/values.yaml" --wait --timeout 5m

echo ">> Installing Grafana"
helm --kube-context "${CONTEXT}" upgrade --install grafana grafana/grafana \
  --version 10.5.15 -n "${NS}" -f "${HERE}/helm/grafana/values.yaml" --wait --timeout 5m

echo ">> Applying dashboard + ingress"
kubectl --context "${CONTEXT}" apply -f "${HERE}/dashboard-rancher-audit.yaml"
kubectl --context "${CONTEXT}" apply -f "${HERE}/ingress.yaml"

echo ">> Done. Grafana at http://grafana.localhost (admin/admin)."
echo ">> Loki push endpoint (from the Mac host): http://localhost/loki/api/v1/push"
