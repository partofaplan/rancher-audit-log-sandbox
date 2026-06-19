#!/usr/bin/env bash
# Deploy the Elasticsearch + Kibana (ELK) logging backend into the bilbo cluster.
#
#   ./bilbo/install.sh
#
# Idempotent: re-run to apply changes. Targets the k3d-bilbo kube context.
set -euo pipefail

CONTEXT="${KUBE_CONTEXT:-k3d-bilbo}"
NS="${NAMESPACE:-monitoring}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo ">> Using context=${CONTEXT} namespace=${NS}"

kubectl --context "${CONTEXT}" create namespace "${NS}" \
  --dry-run=client -o yaml | kubectl --context "${CONTEXT}" apply -f -

echo ">> Applying Elasticsearch + Kibana + ingress"
kubectl --context "${CONTEXT}" apply -f "${HERE}/elk/elasticsearch.yaml"
kubectl --context "${CONTEXT}" apply -f "${HERE}/elk/kibana.yaml"
kubectl --context "${CONTEXT}" apply -f "${HERE}/elk/ingress.yaml"

echo ">> Waiting for Elasticsearch..."
kubectl --context "${CONTEXT}" -n "${NS}" rollout status deploy/elasticsearch --timeout=300s
echo ">> Waiting for Kibana (first boot can take a couple minutes)..."
kubectl --context "${CONTEXT}" -n "${NS}" rollout status deploy/kibana --timeout=420s || true

echo ">> Applying the Rancher audit index template (same one the ELK team applies)"
curl -fsS -X PUT "http://localhost/es/_index_template/rancher-audit" \
  -H 'Content-Type: application/json' \
  --data-binary @"${HERE}/../elk-integration/index-template.json" >/dev/null \
  && echo "   index template applied" || echo "   (could not apply template; is ES reachable on http://localhost/es?)"

echo ">> Setting up Kibana dashboard (from elk-integration/)"
"${HERE}/elk/setup-kibana.sh" || echo "   (Kibana not ready yet — re-run ./bilbo/elk/setup-kibana.sh later)"

echo ">> Done."
echo ">> Kibana:        http://kibana.localhost   (add '127.0.0.1 kibana.localhost' to /etc/hosts)"
echo ">>                Dashboards -> 'Rancher Audit Overview'"
echo ">> Elasticsearch (from the Mac host): http://localhost/es/_cluster/health"
