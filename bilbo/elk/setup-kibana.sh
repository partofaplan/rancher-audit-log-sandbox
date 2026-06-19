#!/usr/bin/env bash
# Import the Kibana saved objects for Rancher auditing: data view, two saved searches
# (translated event sentences + raw log), aggregation visualizations, and the
# "Rancher Audit Overview" dashboard. Idempotent (overwrite=true).
#
# Local bilbo Kibana (default): reached through Traefik on the Mac host with a vhost header.
#   ./setup-kibana.sh
#
# Any other Kibana (e.g. an existing/secured ELK): point KIBANA_URL at it, clear the vhost
# header, and supply credentials (basic auth or an API key):
#   KIBANA_URL=https://kibana.example.com KIBANA_HOST= KIBANA_USER=elastic KIBANA_PASS=... ./setup-kibana.sh
#   KIBANA_URL=https://kibana.example.com KIBANA_HOST= KIBANA_APIKEY=<base64-id:key> ./setup-kibana.sh
#
# Note: the dashboard's data view is titled "rancher-audit"; it must match the ES index your
# AuditLogConfig writes to (spec.elasticsearch.index, default "rancher-audit").
set -euo pipefail

BASE="${KIBANA_URL:-http://localhost}"
HOSTHDR="${KIBANA_HOST-kibana.localhost}"   # set KIBANA_HOST= (empty) for a real DNS host
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Single source of truth for the dashboard is the ELK-team handoff bundle.
NDJSON="${KIBANA_NDJSON:-${HERE}/../../elk-integration/rancher-audit-dashboard.ndjson}"

ARGS=(-fsS -H "kbn-xsrf: true")
[ -n "${HOSTHDR}" ] && ARGS+=(-H "Host: ${HOSTHDR}")
if [ -n "${KIBANA_APIKEY:-}" ]; then
  ARGS+=(-H "Authorization: ApiKey ${KIBANA_APIKEY}")
elif [ -n "${KIBANA_USER:-}" ]; then
  ARGS+=(-u "${KIBANA_USER}:${KIBANA_PASS:-}")
fi

echo ">> Importing Kibana saved objects into ${BASE} from $(basename "$NDJSON")"
RESP=$(curl "${ARGS[@]}" -X POST "${BASE}/api/saved_objects/_import?overwrite=true" \
  --form "file=@${NDJSON}")

echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print('   success=%s count=%s' % (d.get('success'), d.get('successCount'))); [print('   ERROR:', e) for e in d.get('errors',[])]" 2>/dev/null \
  || echo "   response: $RESP"

echo ">> Done. Open Kibana -> Dashboards -> 'Rancher Audit Overview'."
