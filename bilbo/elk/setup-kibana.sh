#!/usr/bin/env bash
# Import the Kibana saved objects for Rancher auditing: data view, two saved searches
# (translated event sentences + raw log), aggregation visualizations, and the
# "Rancher Audit Overview" dashboard. Idempotent (overwrite=true).
#
# Talks to Kibana through bilbo's Traefik on the Mac host using the kibana.localhost
# Host header.
set -euo pipefail

BASE="${KIBANA_URL:-http://localhost}"
HOSTHDR="${KIBANA_HOST:-kibana.localhost}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NDJSON="${HERE}/kibana-objects.ndjson"

echo ">> Importing Kibana saved objects from $(basename "$NDJSON")"
RESP=$(curl -fsS -H "Host: ${HOSTHDR}" -H "kbn-xsrf: true" \
  -X POST "${BASE}/api/saved_objects/_import?overwrite=true" \
  --form "file=@${NDJSON}")

echo "$RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); print('   success=%s count=%s' % (d.get('success'), d.get('successCount'))); [print('   ERROR:', e) for e in d.get('errors',[])]" 2>/dev/null \
  || echo "   response: $RESP"

echo ">> Done. Open Kibana -> Dashboards -> 'Rancher Audit Overview'."
