# Testing

> Unit, integration, and E2E testing conventions for every language in this repo. Read before adding or changing tests.

## Go Unit Tests

- **Framework**: `testify` (`assert`, `mock`, `require`).
- **Mock generation**: `mockery v2.53.3` — regenerate with `make mockgen` after any interface change or CI will fail.
- **Mock output**: `<package>/mocks/mock_<interface>.go`.
- **Naming**: `<module>_test.go` in the same package as the source.
- **Style**: orchestrator / cluster code prefers table-driven tests.
- **Command**: `make test` runs `fmt → vet → lint → go test -coverprofile coverage.out` against every package except `e2e`, `mocks`, and `db/dbtest`.

### Mock directories (from `Makefile` `MOCKERY_OUTPUT_DIRS`)

`testing/mocks`, `pkg/model_registry/mocks`, `pkg/storage/mocks`, `pkg/command/mocks`, `internal/orchestrator/mocks`, `internal/cluster/mocks`, `internal/ray/dashboard/mocks`, `internal/registry/mocks`, `controllers/mocks`, `internal/observability/monitoring/mocks`, `internal/observability/config/mocks`, `internal/gateway/mocks`, `internal/accelerator/mocks`, `internal/auth/mocks`, `internal/util/mocks`.

## Python Tests (co-located)

Python tests use `test_<module>.py` naming and live **in the same directory as the source file** (not a separate `tests/`).

Examples:
- `cluster-image-builder/serve/_utils/coerce.py` → `test_coerce.py`
- `python/neutree/downloader/huggingface.py` → `test_huggingface.py`

When adding or modifying a Python module, create or update the co-located test file.

## Database Integration Tests

- **Location**: `db/dbtest/` — spins up a real PostgreSQL + PostgREST + GoTrue via `db/docker-compose.test.yml`.
- **Command**: `make db-test` (runs migrations + seeds + test, tears down afterwards).
- **Rule**: every migration that touches RLS, permissions, or validation triggers must ship with a matching `db/dbtest/` test.

## E2E Tests

Ginkgo + Gomega.

- **Location**: `tests/e2e/`.
- **Command**: `make e2e-test` — requires `NEUTREE_SERVER_URL` + `NEUTREE_API_KEY` (auto-sourced from `.env` if present).
- **Label filter**: `make e2e-test LABEL_FILTER="<ginkgo-label>"`.

### Conventions

- **Reuse first**: before writing a new E2E, check `tests/e2e/profile.go`, `tests/e2e/helpers.go`, and any `tests/e2e/*_helper.go` for what you need.
- **Split by domain**: `cluster_test.go`, `cluster_fault_test.go`, `endpoint_test.go`, `engine_test.go`, … so each suite runs in isolation.
- **Independent Ginkgo labels**: one label per domain (`cluster`, `fault`, `engine`, …); filter with `--ginkgo.label-filter`.
- **Profile-driven**: infrastructure config lives under `E2E_PROFILE_PATH`; never hard-code environment details.
- **Test data isolation**: use run-id-suffixed resource names, register `DeferCleanup` for anything created.

## File Pairs (implementation and test must change together)

In `internal/orchestrator/` and `internal/cluster/`, an implementation change without a matching update to the `_test.go` in the same commit is incomplete and should be blocked in review.

| Implementation | Test |
|----------------|------|
| `ray_orchestrator.go` | `ray_orchestrator_test.go` |
| `ray_ssh_operation.go` | `ray_ssh_operation_test.go` |
| `kubernetes_orchestrator_resource.go` | `kubernetes_orchestrator_test.go` |
| `ray_ssh_cluster.go` | `ray_ssh_cluster_test.go` |
