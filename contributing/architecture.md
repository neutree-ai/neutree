# Architecture

> Neutree's static architecture: cross-cutting tech stack, layout, layering, and resource model. For per-component depth (request lifecycle, reconcile loop, deploy flow) see:
>
> - [`architecture-neutree-api.md`](architecture-neutree-api.md) — REST API server (Gin)
> - [`architecture-neutree-core.md`](architecture-neutree-core.md) — Control-plane controllers
> - [`architecture-neutree-cli.md`](architecture-neutree-cli.md) — Local deployment CLI

## Tech Stack

- **Language**: Go
- **HTTP framework**: Gin
- **Database**: PostgreSQL + PostgREST
- **Auth**: Supabase GoTrue (JWT / OAuth)
- **CLI**: Cobra
- **Controller framework**: controller-runtime (used by `neutree-core`)
- **Cluster management**: native Kubernetes (K8s mode) / Ray over SSH (static-node mode)
- **Endpoint orchestration**: Kubernetes Deployment (K8s mode) / Ray Application (static-node mode)
- **API gateway**: Kong
- **Observability**: VictoriaMetrics, Grafana, Vector

## Project Layout

```
cmd/                    # Three components — neutree-api / neutree-core / neutree-cli (see per-component docs)
api/v1/                 # L0 — pure resource type definitions
controllers/            # L3 — Kubernetes-style reconcilers (one per resource type)
internal/               # L2 — business logic
pkg/                    # L1 — reusable utilities (consume L0 types only)
db/                     # Migrations (paired up/down) + integration tests + seed
tests/e2e/              # Ginkgo + Gomega E2E suite
cluster-image-builder/  # Engine/runtime image build pipeline (Python serve apps + Dockerfiles)
gateway/kong/plugins/   # Kong Lua plugins (⚠️ not linted)
python/neutree/         # Python model downloaders
deploy/                 # Helm chart + Docker Compose
observability/          # Grafana / vmagent source configs
scripts/                # Engine package builder + dashboard sync
docs/                   # Product / feature design docs (public)
contributing/           # Engineering how-to (this file + sibling docs)
```

Subdirectories worth calling out (the rest are self-explanatory or rarely touched):

| Path | Why |
|------|-----|
| `internal/orchestrator/` | Endpoint orchestration — `kubernetes_orchestrator*.go` vs `ray_orchestrator.go`. Dual-path concern; see [`architecture-neutree-core.md#cluster-modes`](architecture-neutree-core.md#cluster-modes). |
| `internal/cluster/` | Cluster lifecycle — `kubernetes_cluster.go` vs `ray_ssh_cluster.go` + `ray_ssh_operation.go`. Same dual-path concern. |
| `internal/routes/` | Terminal HTTP layer — see boundary rule **R5** below. |
| `internal/accelerator/plugin/` | Vendor plugin pairs (NVIDIA / AMD); see [`architecture-neutree-core.md#vendor-plugin-pairs`](architecture-neutree-core.md#vendor-plugin-pairs). |
| `pkg/scheme/` | Type-registration framework; exempt from the layered hierarchy. |

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
L1  pkg/                  Reusable utilities — storage, model_registry,
                          command_runner, client, command (consume L0 types)
        ↓ may use
L0  api/v1/               Pure resource types + scheme registration
```

> **Note**: `pkg/scheme` is a foundational type-registration framework used by `api/v1` (the Kubernetes-style `apimachinery/runtime` analogue). Treated as Go-runtime-equivalent infrastructure and not part of the layered hierarchy.

Boundary rules currently enforced (see `scripts/check-boundaries.sh` for the implementation and grandfathered exceptions):

| Rule | Statement |
|------|-----------|
| **R1** | `pkg/` (L1) must not import `internal/` (L2). |
| **R2** | `api/v1/` (L0) must not import `internal/` (L2) or `controllers/` (L3). |
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
make mockgen            # Regenerate mocks (mockery)

# Release
make release            # Binaries + Helm chart
```

## Data Flow

Two control-plane processes connect directly to PostgREST; they do not call each other:

```
HTTP CRUD path:    Client (UI/CLI) → neutree-api → PostgREST ⇄ PostgreSQL

Reconcile path:    neutree-core (controllers) ⇄ PostgREST ⇄ PostgreSQL
                                        ↓
                           Orchestrators (K8s / Ray-SSH)
                                        ↓
                                   Endpoints

Inference path:    Client → Kong Gateway → Endpoints
```

`neutree-api` exposes CRUD over HTTP and proxies to PostgREST. `neutree-core` is an independent process that reads/writes the same PostgREST and reconciles desired state by driving the orchestrators. Inference traffic bypasses both control-plane processes and goes through Kong directly.

For per-component internals (route topology, reconcile loop shape, CLI subcommand map) see the per-component docs linked at the top of this file.

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
