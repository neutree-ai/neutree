#!/usr/bin/env bash
# Naive perf comparison: PD same-host endpoint vs a monolithic baseline of the
# same model. Output goes into report.md V9 row.
#
# Requires `vllm bench serve` (ships with vLLM) or `oha`.
#
# Env:
#   PD_URL              — service_url of the Demo PD endpoint
#   MONO_URL            — service_url of a monolithic comparison endpoint
#   MODEL               — served_model name (default Qwen/Qwen2.5-7B-Instruct)
#   REQUESTS            — total request count (default 200)
#   CONCURRENCY         — parallel clients (default 16)
set -euo pipefail

: "${PD_URL:?must be set}"
: "${MONO_URL:?must be set}"
MODEL="${MODEL:-Qwen/Qwen2.5-7B-Instruct}"
REQUESTS="${REQUESTS:-200}"
CONCURRENCY="${CONCURRENCY:-16}"

run() {
  local name="$1" url="$2"
  echo ">>> ${name}: ${url}"
  if command -v vllm >/dev/null 2>&1; then
    vllm bench serve \
      --backend openai-chat \
      --base-url "${url}" \
      --model "${MODEL}" \
      --num-prompts "${REQUESTS}" \
      --max-concurrency "${CONCURRENCY}" \
      --dataset-name random \
      --random-input-len 512 \
      --random-output-len 128 \
      --result-dir "/tmp/neutree-demo-perf-${name}"
  else
    echo "!!! 'vllm bench serve' not found; falling back to oha"
    oha -n "${REQUESTS}" -c "${CONCURRENCY}" \
        -H 'Content-Type: application/json' \
        -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":128}" \
        -m POST "${url}/v1/chat/completions"
  fi
}

run pd   "${PD_URL}"
run mono "${MONO_URL}"

echo ">>> compare report dirs under /tmp/neutree-demo-perf-* for TTFT / TPS."
