#!/usr/bin/env bash
# Create the Kibana data view + saved search for the Rancher audit index.
# Idempotent (fixed object IDs, overwrite=true). Talks to Kibana through bilbo's
# Traefik on the Mac host using the kibana.localhost Host header.
set -euo pipefail

BASE="${KIBANA_URL:-http://localhost}"
HOSTHDR="${KIBANA_HOST:-kibana.localhost}"

kb() { curl -fsS -H "Host: ${HOSTHDR}" -H "kbn-xsrf: true" -H "Content-Type: application/json" "$@"; }

echo ">> Creating data view 'rancher-audit'"
kb -X POST "${BASE}/api/saved_objects/index-pattern/rancher-audit?overwrite=true" -d '{
  "attributes": { "title": "rancher-audit", "name": "Rancher Audit", "timeFieldName": "@timestamp" }
}' >/dev/null

echo ">> Creating saved search 'Rancher Audit Events'"
kb -X POST "${BASE}/api/saved_objects/search/rancher-audit-events?overwrite=true" -d '{
  "attributes": {
    "title": "Rancher Audit Events",
    "columns": ["audit.actor","audit.verb","audit.resource","audit.target","audit.responseCode","audit.summary"],
    "sort": [["@timestamp","desc"]],
    "kibanaSavedObjectMeta": { "searchSourceJSON": "{\"query\":{\"query\":\"\",\"language\":\"kuery\"},\"filter\":[],\"indexRefName\":\"kibanaSavedObjectMeta.searchSourceJSON.index\"}" }
  },
  "references": [{ "id": "rancher-audit", "name": "kibanaSavedObjectMeta.searchSourceJSON.index", "type": "index-pattern" }]
}' >/dev/null

echo ">> Done. Open Kibana -> Discover -> 'Rancher Audit Events'."
