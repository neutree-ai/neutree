# Architecture

> Neutree's static architecture: tech stack, directory layout, deployment modes, and resource model. This is the "map" — for testing, standards, invariants, or playbooks see the sibling files in `contributing/`.

## Tech Stack

- **Language**: Go 1.23
- **HTTP framework**: Gin
- **Database**: PostgreSQL 15 + PostgREST
- **Auth**: Supabase GoTrue (JWT / OAuth)
- **CLI**: Cobra
- **Controller framework**: controller-runtime (used by `neutree-core`)
- **Cluster management**: native Kubernetes 1.26+ (K8s mode) / Ray over SSH (static-node mode)
- **Endpoint orchestration**: Kubernetes Deployment (K8s mode) / Ray Application (static-node mode)
- **API gateway**: Kong
- **Observability**: VictoriaMetrics, Grafana, Vector

## Project Layout

```
cmd/
  neutree-api/          # REST API server (Gin)
  neutree-core/         # Control-plane controllers
  neutree-cli/          # Local deployment CLI

api/v1/                 # Core API type definitions (pure types; must not import internal/ or controllers/)

controllers/            # 12 Kubernetes-style resource controllers (+ base_controller.go)
                        # cluster, endpoint, external_endpoint, engine, image_registry,
                        # model_registry, model_catalog, workspace, role, role_assignment,
                        # api_key, user_profile

internal/
  orchestrator/         # Endpoint orchestration (K8s Deployment / Ray Application)
  cluster/              # Cluster lifecycle (K8s & SSH/Ray)
  engine/               # Inference engine management (vLLM, llama.cpp, ...)
  gateway/              # Kong AI gateway integration
  auth/                 # GoTrue client (login, token issue/refresh)
  middleware/           # HTTP middlewares (JWT verification, api-key-to-JWT)
  routes/               # API route handlers & proxies
  accelerator/          # GPU / accelerator plugins (NVIDIA, AMD)
  ray/                  # Ray dashboard client & helpers
  registry/             # Image registry integration
  observability/        # Metrics / logging config
  cli/                  # CLI helpers shared across neutree-cli commands
  deploy/               # Control-plane deploy/launch helpers (used by neutree-cli)
  nfs/                  # NFS model-cache helpers
  cron/                 # Scheduled jobs
  semver/, util/, utils/, version/   # Small shared utilities

pkg/
  storage/              # PostgREST storage layer
  model_registry/       # HuggingFace & file model registries
  command_runner/       # SSH / Docker / K8s command execution
  command/              # Command abstractions
  client/               # External API clients
  scheme/               # Type scheme registration

db/
  migrations/           # Up/down paired migrations (NNN_*.{up,down}.sql)
  dbtest/               # DB integration tests (RLS, triggers)
  seed/                 # Seed data for test / local
  init-scripts/         # Bootstrap SQL
  docker-compose.test.yml

tests/e2e/              # Ginkgo + Gomega E2E suite

cluster-image-builder/  # Engine/runtime image build pipeline
  serve/                # Python serve apps per engine/version (vLLM, llama.cpp)
  accelerator/          # GPU accelerator helpers
  Dockerfile.engine-*   # Per-engine images

gateway/kong/
  plugins/              # Kong Lua plugins (⚠️ not linted)

python/neutree/
  downloader/           # HuggingFace + local model downloaders

deploy/
  chart/                # Helm chart
  docker/               # Docker compose

observability/
  grafana/              # Dashboards source
  vmagent/              # VictoriaMetrics agent configs

scripts/
  builder/              # Engine package builder (build-engine-package.sh)
  dashboard/            # Grafana dashboard sync

docs/                   # Product / feature design docs (public)
contributing/           # Engineering how-to (this file + testing / coding-standards / invariants / database / playbooks)
```

## Layered Architecture

Code is organized into five layers with a **downward-only dependency rule**. Higher layers may import lower layers; lower layers must never import higher ones. The boundaries that can be checked mechanically are enforced by the pre-commit hook.

```text
L4  cmd/                  Entry points — assembly only, no business logic
        ↓ may use
L3  controllers/          Kubernetes-style reconcilers
        ↓ may use
L2  internal/             Business logic, divided by role
                          ├── L2-domain  (cluster, orchestrator, engine, gateway, auth, ...)
                          ├── L2-edge    (routes, middleware)
                          └── L2-shared  (util, semver, version, observability, ...)
        ↓ may use
L1  api/v1/               Pure type definitions; no business logic
        ↓ may use
L0  pkg/                  Reusable libraries; must not couple to neutree internals
```

Boundary rules currently enforced (see `scripts/check-boundaries.sh` for the implementation and grandfathered exceptions):

| Rule | Statement |
|------|-----------|
| **R1** | `pkg/` (L0) must not import `internal/` (L2). |
| **R2** | `api/v1/` (L1) must not import `internal/` (L2) or `controllers/` (L3). |
| **R3** | `internal/orchestrator/` (L2-domain) must not import `internal/routes/` (L2-edge). |
| **R4** | `cmd/` files (L4) must stay under 500 lines — entry points are wiring, not business logic. |
| **R5** | No package outside `internal/routes/` may import `internal/routes/` — routes is a terminal HTTP layer. |

R3 is a specialization of R5; both are kept so contributors immediately see the dual-orchestrator constraint.

## Common Commands

```bash
# Build
make build              # All binaries
make docker-build       # Multi-arch images

# Test
make test               # Unit tests (see contributing/testing.md)
make db-test            # DB integration tests
make e2e-test           # E2E (requires NEUTREE_SERVER_URL + NEUTREE_API_KEY)

# Code quality
make lint               # golangci-lint (see contributing/coding-standards.md)
make fmt                # go fmt
make vet                # go vet
make mockgen            # Regenerate mocks (mockery v2.53.3)

# Release
make release            # Binaries + Helm chart
```

## Controller Pattern

Neutree models every resource with the Kubernetes controller pattern, using the controller-runtime framework inside `neutree-core`.

- Controllers continuously reconcile **Spec** (desired state, user-written) against **Status** (observed state, controller-written).
- **Idempotent**: controllers must be safe to run repeatedly; a second pass on the same inputs must not cause extra side effects.
- **Soft delete**: resources are first marked with `deletion_timestamp`, the controller runs cleanup, then the row is permanently removed.
- **Spec/Status separation**: users write only Spec; Status is owned by the controller and never mutated by clients.

## Cluster Modes

Two deployment topologies share the same `Cluster` resource API, selected via `spec.config`:

1. **Kubernetes mode**: cluster lifecycle is delegated to the native Kubernetes control plane. Neutree installs only the inference infrastructure (router, metrics collector) in a dedicated namespace. Endpoints run as native Kubernetes Deployments. Code paths: `internal/orchestrator/kubernetes_orchestrator*.go`, `internal/cluster/kubernetes_cluster.go`.
2. **Static-node mode**: Ray over SSH provisions the cluster (each Ray node runs inside a Docker container). Endpoints deploy as Ray Applications on top of Ray Serve. Code paths: `internal/orchestrator/ray_orchestrator.go`, `internal/cluster/ray_ssh_cluster.go` + `ray_ssh_operation.go`.

Endpoint-level changes that touch one mode often need a parallel change in the other; the pre-commit hook (`scripts/check-dual-path.sh`) emits an advisory warning when only one side is touched. It is not a block — if the change applies to only one mode, note the reason in the PR body.

## Vendor Plugin Pairs

`internal/accelerator/plugin/gpu.go` (NVIDIA) and `internal/accelerator/plugin/amd_gpu.go` implement the same accelerator interface with vendor-specific details. A change to one must be evaluated against:

- The other vendor implementation.
- `internal/accelerator/plugin/client.go` and `plugin.go` — the shared interface layer that frequently needs updates when vendor-specific logic changes.

## Data Flow

```
Client → neutree-api → PostgREST → PostgreSQL
              ↓
         neutree-core (12 controllers)
              ↓
         Orchestrators (K8s / Ray-SSH)
              ↓
         Kong Gateway → Model Inference
```

## Core Resource Types

| Resource | Purpose |
|----------|---------|
| `Cluster` | Cluster configuration (K8s or static-node) |
| `Endpoint` | Inference service endpoint deployed inside a Neutree cluster |
| `ExternalEndpoint` | Reference to an externally hosted inference endpoint |
| `Engine` | Inference engine (vLLM, llama.cpp, ...) |
| `ImageRegistry` | Container image registry credentials |
| `Workspace` | Multi-tenant isolation |
| `Role` / `RoleAssignment` | RBAC permissions |
| `APIKey` | API key management |
| `ModelRegistry` / `ModelCatalog` | Model registration and catalog |
| `UserProfile` | User profile metadata |

Every resource follows the same shape: `api/v1/<resource>_types.go` defines the type → `db/migrations/NNN_*.sql` defines the table + RLS → `controllers/<resource>_controller.go` reconciles → `internal/routes/proxies/<resource>_proxy.go` exposes the API. The full walkthrough lives in [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type).

## Language Distribution

| Language | Purpose | Location |
|----------|---------|----------|
| Go | Control plane, API, CLI, controllers | `cmd/`, `internal/`, `controllers/`, `api/` |
| Python | Serve apps, model downloaders | `cluster-image-builder/serve/`, `python/neutree/` |
| Lua | Kong gateway plugins | `gateway/kong/plugins/` |
| SQL | Database schema, RLS policies | `db/migrations/` |
| YAML | Helm charts, deploy templates | `deploy/`, `internal/engine/*/templates/` |
