# PD Same-Host Design Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align the current `feat-pd-same-host-demo` branch with `feature/pd-same-host-phase1/design/pd-same-host-detail-design-zh.md`.

**Architecture:** Keep the existing Endpoint API, orchestrator, engine template, and log API shapes. Fill the remaining gaps by extending the existing DB composite schema, Ray P/D runtime metadata, Endpoint log source discovery, status aggregation, and dashboard queries before taking on the larger K8s/SGLang runtime slices.

**Tech Stack:** Go control plane, PostgreSQL migrations, Ray Serve Python apps, Kubernetes templates, Grafana dashboards, existing Neutree test harnesses.

---

### Task 1: Persist `EndpointSpec.KV`

**Files:**
- Modify: `db/migrations/060_endpoint_pd_same_host_demo.up.sql`
- Modify: `db/migrations/060_endpoint_pd_same_host_demo.down.sql`
- Test: existing API/schema tests that exercise EndpointSpec round-trip behavior

- [x] Add a failing test or schema assertion showing `spec.kv.transfer` survives the DB composite path.
- [x] Add `kv` to `api.endpoint_spec` in migration 060 up/down.
- [x] Run targeted Go tests for API/internal endpoint spec serialization.

### Task 2: Expose Ray P/D Role Actor Logs

**Files:**
- Modify: `cluster-image-builder/serve/vllm/v0_17_1/app_pd_collocated.py`
- Modify: `cluster-image-builder/serve/vllm/v0_20_0/app_pd_collocated.py`
- Modify: `internal/routes/logs/endpoint_logs.go`
- Test: endpoint logs route tests and Python syntax checks

- [x] Add failing tests for role actor log source discovery from deterministic actor names.
- [x] Pass workspace, endpoint, and Serve replica runtime key into the P/D backend runtime metadata.
- [x] Name `PrefillActor` and `DecodeActor` as `neutree:{workspace}:{endpoint}:replica:{role_group_key}:role:{role}:rank:{rank}`.
- [x] Query Ray State API by actor name, prefer ALIVE actors, and expose role/rank/actor_name/actor_id on log items.
- [x] Run targeted Go tests and Python compile checks.

### Task 3: Improve PD Status Precision

**Files:**
- Modify: `internal/orchestrator/ray_orchestrator.go`
- Modify: `internal/orchestrator/kubernetes_orchestrator.go` if the current K8s state can be safely decorated without adding the full K8s PD runtime.
- Test: orchestrator status tests

- [x] Add failing tests for RoleGroup status using runtime replica keys where available.
- [x] Populate Ray PD `EndpointStatus.Replicas` from actual `PDCollocatedBackend` replica state instead of purely synthetic IDs.
- [x] Add K8s PD status decoration only if existing Kubernetes deployment/pod status APIs already expose the needed data.

### Task 4: Update Monitoring Surfaces

**Files:**
- Modify: `observability/grafana/dashboards/vllm_grafana_dashboard.json`
- Modify: `observability/grafana/dashboards/sglang_grafana_dashboard.json`
- Test: JSON validation and dashboard grep checks

- [x] Add `role` and `rank` dashboard variables or filters.
- [x] Add P/D role health and KV cache/transfer rows using engine-specific metric names.
- [x] Validate dashboard JSON.

### Task 5: K8s/SGLang PD Runtime Slice

**Files:**
- Likely modify: `internal/engine/builtin.go`
- Likely modify: `internal/engine/vllm/*/templates/kubernetes/*`
- Likely create/modify: K8s sidecar translator code/templates
- Likely modify: SGLang Ray app/capability files

- [x] Re-read design sections 5.4.1, 5.5.4, and 5.6.2 before editing.
- [x] Stop and ask for confirmation if implementing the full K8s multi-container Pod and production-stack router contract requires files outside this repo or a new sidecar image contract.
- [ ] Implement the smallest independently testable slice after confirmation.

### Task 6: Verification and PR Hygiene

**Files:**
- Modify only files touched by the tasks above.

- [x] Run `gofmt` where needed.
- [x] Run targeted Go tests.
- [x] Run Python compile checks for touched app files.
- [x] Run dashboard JSON validation after dashboard changes.
- [x] Run `git diff --check`.
- [ ] Commit and push after verified.
