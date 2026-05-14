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
| V14 | `_SHARED` callbacks (push) populate `actor_topology` before any client request | hit `/v1/topology` *before* sending any `/v1/chat/completions`; `actor_topology_count == num_replicas`; ingress log shows `_on_replica_added` invoked for each replica; absence of `[PDIngress] topology pull failed` errors | | |
| V15 | direct dispatch via `multiplexed_model_id == "replica:<rid>"` deterministically routes to the named replica (namespaced so LoRA / SGLang custom routing can coexist on the same channel) | `v15_direct_dispatch_unique == true` in `/v1/topology` (N unique prefill + decode actor_ids, no collisions); ingress log shows `[ObserverRouter] direct dispatch -> <rid>` for each replica_added pull; absence of `direct target ... not in candidates` warnings during steady state | | |
| V16 | Ray Serve 2.53 native rank (`serve.get_replica_context().rank`) populates `_SHARED.actor_topology` with contiguous global_rank values, replacing the need for a custom coordinator actor for replica indexing | `v16_native_rank_populated == true` in `/v1/topology` (sorted `global_rank` array equals `[0, 1, ..., N-1]`); `world_sizes` is a single-element array equal to `N`; backend log shows `[PrefillActor] ready: actor_id=...` with consistent rank assignment | | |
| V17 | End-to-end portalloc: each actor's `VLLM_NIXL_SIDE_CHANNEL_PORT` comes from the control-plane allocator (no engine defaults, no per-actor collisions across replicas on the same host) | docker inspect (or `nsenter` on host) shows each PrefillActor / DecodeActor process bound to a unique port in `ClusterSpec.PortRange` (20000-29999 default); ingress log shows no `port collision` / `bind address already in use` errors during scale up to NumReplicas > 1 on a single GPU host | | |
| V18 | xPyD parameterized: prefill `Instances=x` and decode `Instances=y` materialize as x+y inner actors per replica, all STRICT_PACK on the same node; pd_chat round-robins across them | `prefill_counts_per_replica == [x]` and `decode_counts_per_replica == [y]` in `/v1/topology`; backend log shows `[PDCollocatedBackend] PG ready: xP + yD = (x+y) bundles`; sending N×(x+y) sequential requests results in each PrefillActor receiving ~N hits per actor (round-robin) | | |
| V19 | User-supplied `Role.Env` / `Role.Variables` win over platform-derived values on key collision; non-conflicting platform values still propagate | set `roles[].env.VLLM_LOGGING_LEVEL=DEBUG` and `roles[].variables.max_num_seqs=128` in EndpointSpec; verify both reach the actor process (`docker exec ... env / vllm engine log`); set a collision case like `roles[].env.VLLM_NIXL_SIDE_CHANNEL_PORT=99999` and check ingress log shows `user overriding platform-controlled key` warning and the actor binds 99999 (i.e. user wins, audit trail present) | | |

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
| V14 fails | push path doesn't pre-populate `actor_topology` → check whether `_SHARED.replace_replicas` actually fired from `ObserverRouter.update_replicas` in the same process (logs); fall back to safety-net lazy refresh; consider moving callback dispatch onto a dedicated event loop in MVP |
| V15 fails | `multiplexed_model_id` not surfaced to choose_replicas (Ray version regression?) → confirm `pending_request.metadata.multiplexed_model_id` is set; if absent, fall back to named-actor topology registry pattern; without V15, MVP composite-key dispatch (decode_rid + prefill_rid) needs a different metadata channel |
| V16 fails | `serve.get_replica_context().rank` absent or all -1 → Ray version regression (rank added in 2.53); fall back to detached coordinator actor pattern (Approach A from rank analysis); without V16, MVP port allocation needs out-of-band rank coordination |
| V17 fails | actor process bound to `5500` (vLLM default) or refuses to start with "plan.Ports required" → portalloc not wired (orchestrator.Options.PortAllocator is nil) OR strategy.PD.Compile didn't set Role.PortsPerRank; cross-check `cluster_port_allocations` table for rows matching the endpoint id |

Fill the rows above, link captured logs (`docker logs`, `ray serve status`,
`nvidia-smi nvlink -s` output) under `Notes / artifact`, then attach the
final report to GitLab MR !21.
