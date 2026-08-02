# Testing

> Unit, integration, and E2E testing conventions for every language in this repo. Read before adding or changing tests.

## Go Unit Tests

- **Framework**: `testify` (`assert`, `mock`, `require`).
- **Mock generation**: `mockery v2.53.3` — regenerate with `make mockgen` after any interface change or CI will fail.
- **Mock output**: `<package>/mocks/mock_<interface>.go`.
- **Naming**: `<module>_test.go` in the same package as the source.
- **Style**: prefer table-driven tests for all packages — one `cases` slice with subtests via `t.Run(tc.name, ...)`.
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

## Iterating Against a Running Control Plane

Some changes can only be exercised against a real deployment — the control plane
needs PostgreSQL / PostgREST / GoTrue, and the orchestrators need a real cluster.
The loop is: build a static binary, swap it into the running container, restart,
check it came back healthy.

### Local containers

`make docker-test-api` / `make docker-test-core` rebuild the binary and
`docker cp` it into a container on the **local** Docker daemon, then restart it.
No backup, no verification — they are meant for a throwaway local stack.

### Remote hosts

`make deploy-remote` does the same against a container on another host over SSH,
and adds the parts you want when the target is not disposable: it backs the
current binary up first, verifies the transfer, installs it without tripping
`ETXTBSY`, and checks the container actually came back.

```bash
# Stock container — the binary lives in the container's own filesystem
make deploy-remote HOST=root@10.0.0.5 COMP=neutree-api

# Bind-mount deployment — REMOTE_BIN is the host directory mounted into the container
make deploy-remote HOST=root@10.0.0.5 COMP=neutree-core REMOTE_BIN=/opt/neutree/bin

# Undo it
make deploy-remote-rollback HOST=root@10.0.0.5 COMP=neutree-api
```

| Variable | Default | Meaning |
|---|---|---|
| `HOST` | *(required)* | SSH target, e.g. `root@10.0.0.5`. |
| `COMP` | *(required)* | `neutree-api` or `neutree-core`. Also the default container name and the in-container binary path (`/$(COMP)`). |
| `REMOTE_BIN` | *(empty)* | **Empty → `docker cp` mode.** Set → bind-mount mode; the directory on `HOST` mounted into the container, where `$(REMOTE_BIN)/$(COMP)` is the file the container executes. |
| `CONTAINER` | `$(COMP)` | Container name, when it differs from the component name. |
| `BACKUP_DIR` | `/var/tmp/neutree-deploy-remote` | Where the previous binary is kept on `HOST`. Deliberately outside `REMOTE_BIN` so backups are not visible inside the container. |
| `HEALTH_URL` | *(empty)* | HTTP probe, curled **from `HOST`** after the restart. Skipped (loudly) when empty, because the right URL and port are deployment-specific. |
| `SETTLE` | `10` | Seconds to wait after the restart before checking — long enough for a crash loop to show itself. |
| `SSH_OPTS` | *(empty)* | Extra flags for `ssh`/`scp`, e.g. `-i ~/.ssh/id_ed25519 -o StrictHostKeyChecking=no`. |

**Which mode do I want?** `docker cp` mode is the default because it matches a
stock `neutree-cli launch` / Compose deployment, where the binary ships inside
the image. Bind-mount mode only applies if whoever set the host up mounted a
host directory over the binary. `docker inspect -f '{{json .HostConfig.Binds}}'
<container>` tells you which one you are looking at; if the binary path is not
in there, leave `REMOTE_BIN` empty.

Why each step is there — every one of these has bitten someone:

- **Backup before touching anything.** `deploy-remote-rollback` restores it and
  restarts. Nothing else in the flow can un-break a bad binary.
- **`chmod 755` after transfer.** `scp` propagates the *local* file's mode, so a
  binary that lost its executable bit locally arrives non-executable and the
  container comes back up with a permission denied on exec. `curl` writes `0644`
  by default, as do most archive extractions, so this bites whenever `bin/$(COMP)`
  came from somewhere other than a local `go build`. Setting the bit explicitly
  makes the target independent of how the binary was produced.
- **Stage to a temp file, then `mv -f`.** Writing over a running executable in
  place fails with `ETXTBSY` (`cp: cannot create regular file ...: Text file
  busy`). A rename swaps the directory entry instead, which the running process
  does not care about. The temp file is staged *inside* `REMOTE_BIN` on purpose:
  across a filesystem boundary `mv` degrades from `rename(2)` to a copy, which
  still gets past `ETXTBSY` — GNU `mv` unlinks the target and retries — but is
  no longer atomic, leaving a window where the binary is missing or half-written.
- **Checksum both ends.** A truncated transfer otherwise shows up as a confusing
  crash loop.
- **`RestartCount` must not move.** Not `== 0`: the count is cumulative over the
  container's whole life and `docker restart` does not reset it, so any host
  that ever had a crash fails an absolute check. What matters is that the
  restart policy did not kick in *after* the deploy.
- **`--version` must report the commit just built.** This is what catches "the
  swap silently didn't take". It relies on the version ldflags in
  `GO_BUILD_ARGS`, which is why the target builds through `make build-$(COMP)`.
  A binary built with a bare `go build` reports `Git Commit: unknown` and fails
  this check — that is the check working, not a bug.
- **Logs scanned for `panic:` / fatal.** A process can be `Running` and still
  have logged a fatal error on a background goroutine.

The target carries no environment-specific defaults: `HOST` and `COMP` have to
be supplied on every invocation.

