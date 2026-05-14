# PD Same-Host Phase 0 — Validation Report

Fill this after running `create.sh` + `verify.sh` (+ optional `perf.sh`).
Each row maps to a Demo assumption (V1–V9) from
[`00-overview-and-pr-plan.md §3.0`](../../knowledge/neutree-pd-same-host-phase1-detailed/00-overview-and-pr-plan.md).

| # | Assumption | How to read the signal | Pass / Fail | Notes / artifact |
|---|---|---|---|---|
| V1 | API → IR → Orchestrator → Ray Application end-to-end works | `verify.sh` reaches `phase=Running` and Ray dashboard shows Application `${EP}` Running | | |
| V2 | `(strategy, placement.roles)` routes to PD import_path | endpoint_controller logs show `applyPDBranch` invocation; Ray Application `import_path` = `serve.vllm.v0_17_1.app_pd_collocated:app_builder` | | |
| V3 | Plan serialized to Python args dict reaches deserialization intact | `app_pd_collocated.py` log line `placement_group ready` AND `engine_kwargs={...}` matches the EndpointSpec | | |
| V4 | STRICT_PACK PG colocates prefill + decode actors on same node | inside the head pod: `ray.get(pg.bundle_specs)` shows 2 bundles; both Actor.get_node_id are equal | | |
| V5 | PrefillActor / DecodeActor NIXL cuda_ipc handshake works | engine log shows `NixlConnector ... role=kv_producer/kv_consumer` + a successful handshake; `nvidia-smi nvlink -s` shows nonzero RX/TX on the same host | | |
| V6 | `kv_transfer_params` is a plain dict, round-trippable through Ray | `pd_chat` log shows `kv_params={...}` with no exception; field types are JSON-friendly | | |
| V7 | Ray Serve handle dispatch latency < 1 ms cross actor | `verify.sh` e2e latency vs vLLM stand-alone; Ray Serve metrics `ray_serve_request_router_duration_seconds_bucket` p50 < 0.001 | | |
| V8 | Streaming dispatch does not buffer the full decode response | `verify.sh` with `stream=true` (toggle in the script) prints incremental chunks; PDIngress mem stays flat | | |
| V9 | NIXL cuda_ipc bandwidth approaches NVLink theoretical | `perf.sh` PD TTFT vs monolithic baseline; document GB/s observed | | |
| V10 | ObserverRouter sees N PDCollocatedBackend replicas via `update_replicas` | `verify.sh` calls `/v1/topology` and `serve_replicas_count == num_replicas`; ingress log shows `[ObserverRouter] update_replicas: total=N` | | |
| V11 | replica add/remove drives ObserverRouter callbacks → `_SHARED` updates | scale endpoint to N+1 (or kill a backend replica); poll `/v1/topology` until count changes; check ingress log for `[ObserverRouter] update_replicas` / `replica died:` | | |
| V12 | actor_topology cache keys by canonical Ray Serve ReplicaID | `verify.sh` shows `all_replica_ids_keyed == true` (the dict keys equal the `replica_id` field inside each entry) | | |
| V13 | each replica's actor identity (actor_id + node_id + gpu_ids) reaches `_SHARED.actor_topology` | `prefill_actor_ids` and `decode_actor_ids` arrays have N unique non-empty values; `prefill_gpu_ids` / `decode_gpu_ids` non-empty per entry; backend log shows `[PrefillActor] ready: actor_id=... node=... gpus=[...]` | | |

## Decision matrix (feeds MVP planning)

| Outcome | Next action |
|---|---|
| V1–V4 all pass | Architecture is sound → proceed to MVP scope as planned in `00 doc §MVP` |
| V5 fails | NIXL handshake stalled → +5d to MVP for transport debugging; consider Mooncake fallback |
| V6 fails | kv_transfer_params not plain dict → +2d for serialization shim |
| V7 fails | Cross-actor dispatch > 1 ms → reconsider whether PDIngress should fuse with PDCollocatedBackend |
| V8 fails | Streaming buffered in ingress → +3d streaming rewrite |
| V9 fails | Bandwidth far from NVLink theoretical → investigate fabric manager / NVSwitch routing before MVP |
| V10 fails | RequestRouter callback never fires → MVP design assumption invalid; redesign as a Ray detached actor topology service before PR-ingress-lib |
| V11 fails | replica death/scale not reflected → consider Ray Serve internal hook stability; potentially poll backend status from MVP CP side instead |
| V12 fails | `serve.get_replica_context()` returns empty / unstable id across Ray versions → fall back to a stable id minted by PDCollocatedBackend.__init__ (e.g. uuid4) and emit it from both ObserverRouter (via a side channel) and get_actor_topology |
| V13 fails | per-actor identity not exposed via runtime_context → switch to actor-side `ray.get_runtime_context()` probing OR have actors register themselves in a named topology actor |

Fill the rows above, link captured logs (`docker logs`, `ray serve status`,
`nvidia-smi nvlink -s` output) under `Notes / artifact`, then attach the
final report to GitLab MR !21.
