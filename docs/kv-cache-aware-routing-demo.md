# KV-aware routing MVP

This demo enables event-aware routing for vLLM 0.24 endpoints on Ray/static
clusters. Each Backend replica consumes its own vLLM KV event stream, reports a
bounded block-index snapshot through Ray routing stats, and the request router
prefers the replica with the longest resident prompt prefix. GPU and native CPU
offload events feed the same replica-level index; routing does not model tiers.

## Scope

- vLLM 0.24.0 and the Neutree Ray 2.53.0 fork
- Ray/static clusters
- text-only chat requests without LoRA or `cache_salt`
- `data_parallel_size=1` per Backend replica
- local GPU cache plus vLLM native CPU offloading

Unsupported requests or missing/stale index state automatically use Ray's
normal queue-length routing.

## Build the engine image

The built-in vLLM 0.24 EngineVersion expects the tag
`v0.24.0-neutree1-ray2.53.0`. Build and publish that image from this branch:

```bash
cd cluster-image-builder
make docker-build-engine-vllm \
  IMAGE_REPO=<registry-host> \
  IMAGE_PROJECT=neutree \
  ENGINE_PATCH_SUFFIX=1
docker push <registry-host>/neutree/engine-vllm:v0.24.0-neutree1-ray2.53.0
```

Confirm that the ImageRegistry used by the target cluster resolves
`neutree/engine-vllm` through `<registry-host>` before creating the endpoint.

## Endpoint configuration

Use two replicas and set the deployment scheduler to `kv_aware`. The wrapper
automatically configures an isolated IPC event endpoint for each replica, so do
not set `kv_events_config` manually. For native CPU offloading, it also makes
the CPU events self-describing so they can enter the same block index.

```yaml
spec:
  engine:
    engine: vllm
    version: v0.24.0
  replicas:
    num: 2
  variables:
    engine_args:
      enable_prefix_caching: true
      kv_offloading_size: 4
      kv_offloading_backend: native
  deployment_options:
    scheduler:
      type: kv_aware
      stats_period_s: 0.5
      stats_timeout_s: 2.0
      max_index_age_s: 5.0
      min_matched_blocks: 1
      max_index_blocks: 8192
```

Apply this section to a normal model-specific Endpoint manifest and wait for the
Endpoint to become `Ready` using `neutree-cli`.

## Smoke test

Run the deterministic shared-prefix workload after both Backend replicas are
ready:

```bash
python3 scripts/kv-cache-aware-demo.py \
  --label backend-index \
  --base-url http://<gateway>/<workspace>/<endpoint> \
  --model <served-model-name> \
  --churn-requests 16
```

Expected evidence:

1. Backend logs contain `KV-aware routing enabled` for both replicas.
2. After warmup and the routing-stats delay, Router logs contain
   `KV-aware routing matched <N> blocks` with `N > 0`.
3. Existing vLLM metrics/logs show native CPU offloading is active. The Router
   deliberately treats CPU and GPU copies as the same replica-local residency.

For before/after data, deploy an otherwise identical endpoint with
`scheduler.type: consistent_hash`, run the same script against both endpoints,
and compare the script's median/p95 TTFT and end-to-end latency. The MVP proves
event-aware replica selection; it does not optimize CPU-versus-GPU placement.

## Local tests

The core index and production router can be tested without Ray or vLLM installed:

```bash
PYTHONPATH=cluster-image-builder \
  python3 cluster-image-builder/serve/_kv_cache/test_local_index.py -v
PYTHONPATH=cluster-image-builder \
  python3 cluster-image-builder/serve/_replica_scheduler/test_kv_aware_scheduler.py -v
python3 -m compileall -q \
  cluster-image-builder/serve/_kv_cache \
  cluster-image-builder/serve/_replica_scheduler/kv_aware_scheduler.py \
  cluster-image-builder/serve/vllm/v0_24_0/app.py
```
