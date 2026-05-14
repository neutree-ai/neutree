#!/usr/bin/env bash
# Submit the Demo PD same-host endpoint to Neutree CP.
#
# Env:
#   NEUTREE_API_URL  — e.g. http://10.255.1.54:3000
#   NEUTREE_API_KEY  — Bearer API key
#   CLUSTER          — registered SSH cluster name
#
# Output:
#   endpoint POST response (HTTP 201 on success)
set -euo pipefail

: "${NEUTREE_API_URL:?must be set}"
: "${NEUTREE_API_KEY:?must be set}"
: "${CLUSTER:?must be set, registered cluster name}"

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &>/dev/null && pwd)"
body="$(sed "s/REPLACE_WITH_CLUSTER_NAME/${CLUSTER}/g" "${here}/endpoint.json")"

echo ">>> POST ${NEUTREE_API_URL}/api/v1/endpoints"
echo "${body}" | jq .

curl -fsS \
  -H "Authorization: Bearer ${NEUTREE_API_KEY}" \
  -H "Content-Type: application/json" \
  -X POST \
  "${NEUTREE_API_URL}/api/v1/endpoints" \
  --data-raw "${body}" \
  | jq .
