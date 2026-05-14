#!/usr/bin/env bash
# Poll Neutree CP until the Demo PD endpoint is Running, then smoke /v1/models
# and /v1/chat/completions through the gateway. Captures the V1/V2/V3/V7 signals.
#
# Env:
#   NEUTREE_API_URL, NEUTREE_API_KEY  — Neutree CP
#   WAIT_SECONDS                       — default 600 (10 min model download budget)
#   PROMPT                             — default "Explain prefill/decode in one paragraph."
set -euo pipefail

: "${NEUTREE_API_URL:?must be set}"
: "${NEUTREE_API_KEY:?must be set}"
WAIT_SECONDS="${WAIT_SECONDS:-600}"
PROMPT="${PROMPT:-Explain prefill/decode in one paragraph.}"

NAME="demo-pd-same-host"
WORKSPACE="default"

echo ">>> polling endpoint phase (max ${WAIT_SECONDS}s)"
deadline=$(( $(date +%s) + WAIT_SECONDS ))
while :; do
  state=$(curl -fsS \
    -H "Authorization: Bearer ${NEUTREE_API_KEY}" \
    "${NEUTREE_API_URL}/api/v1/endpoints?metadata.workspace=eq.${WORKSPACE}&metadata.name=eq.${NAME}" \
    | jq -r '.items[0].status.phase // "Unknown"')
  echo "  phase=${state}"
  case "${state}" in
    Running) break ;;
    Failed)
      echo "!!! endpoint Failed"
      curl -fsS \
        -H "Authorization: Bearer ${NEUTREE_API_KEY}" \
        "${NEUTREE_API_URL}/api/v1/endpoints?metadata.workspace=eq.${WORKSPACE}&metadata.name=eq.${NAME}" \
        | jq '.items[0].status'
      exit 1 ;;
  esac
  if (( $(date +%s) >= deadline )); then
    echo "!!! deadline exceeded"
    exit 1
  fi
  sleep 5
done

echo ">>> resolving service_url"
svc=$(curl -fsS \
  -H "Authorization: Bearer ${NEUTREE_API_KEY}" \
  "${NEUTREE_API_URL}/api/v1/endpoints?metadata.workspace=eq.${WORKSPACE}&metadata.name=eq.${NAME}" \
  | jq -r '.items[0].status.service_url')
echo "  service_url=${svc}"

echo ">>> GET ${svc}/v1/models"
curl -fsS "${svc}/v1/models" | jq .

echo ">>> GET ${svc}/v1/topology (V10/V11: ObserverRouter view of replicas)"
# First call also primes the actor_topology cache via lazy pull.
curl -fsS "${svc}/v1/topology" | jq .
echo ">>> sleep 5s then re-poll to confirm cache populated"
sleep 5
curl -fsS "${svc}/v1/topology" | jq '{
  last_update_ts,
  serve_replicas_count: (.serve_replicas | length),
  actor_topology_count: (.actor_topology | length),
  all_same_host:        ([.actor_topology[].same_host] | all),
  all_replica_ids_keyed: (
    [.actor_topology[].replica_id] == (.actor_topology | keys)
  ),
  # V15: direct dispatch via multiplexed_model_id must yield unique actor
  # identities across the cluster — total prefill_actors == NumReplicas * x
  # and total decode_actors == NumReplicas * y, all distinct.
  v15_direct_dispatch_unique: (
    ([.actor_topology[].prefills[].actor_id] | length)
      == ([.actor_topology[].prefills[].actor_id] | unique | length)
    and
    ([.actor_topology[].decodes[].actor_id] | length)
      == ([.actor_topology[].decodes[].actor_id] | unique | length)
  ),
  # V16: Ray Serve 2.53 native rank populated from serve.get_replica_context().rank
  # global_rank must be contiguous 0..N-1 across all replicas.
  v16_native_rank_populated: (
    [.actor_topology[].global_rank] | sort
      == [range(0; (.actor_topology | length))]
  ),
  global_ranks:         [.actor_topology[].global_rank] | sort,
  world_sizes:          [.actor_topology[].world_size]  | unique,
  prefill_actor_ids:    [.actor_topology[].prefills[].actor_id] | unique,
  decode_actor_ids:     [.actor_topology[].decodes[].actor_id]  | unique,
  prefill_counts_per_replica: [.actor_topology[] | .prefills | length] | unique,
  decode_counts_per_replica:  [.actor_topology[] | .decodes  | length] | unique,
  prefill_gpu_ids:      [.actor_topology[].prefills[].gpu_ids],
  decode_gpu_ids:       [.actor_topology[].decodes[].gpu_ids],
  prefill_nodes:        [.actor_topology[].prefills[].node_id] | unique,
  decode_nodes:         [.actor_topology[].decodes[].node_id]  | unique
}'

echo ">>> POST ${svc}/v1/chat/completions (smoke)"
start=$(date +%s%N)
resp=$(curl -fsS -H 'Content-Type: application/json' \
  -X POST "${svc}/v1/chat/completions" \
  -d "{\"model\": \"Qwen/Qwen2.5-7B-Instruct\", \"messages\": [{\"role\":\"user\",\"content\":\"${PROMPT}\"}], \"max_tokens\": 64}")
end=$(date +%s%N)
echo "${resp}" | jq .
echo ">>> e2e latency: $(( (end-start) / 1000000 )) ms"
