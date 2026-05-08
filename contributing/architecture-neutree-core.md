# neutree-core

## Process Role

`neutree-core` is the second of two independent control-plane processes. It runs Kubernetes-style controllers built on `controller-runtime`. Each controller reads desired state (`Spec`) and observed state (`Status`) from PostgREST and drives the system toward convergence — provisioning clusters, deploying endpoints, syncing image registries, etc.

It does **not**:
- serve HTTP traffic (that's `neutree-api`),
- talk to `neutree-api` directly (both processes connect to PostgREST independently — see [`architecture.md#data-flow`](architecture.md#data-flow)),
- handle inference traffic.

Entry point: `cmd/neutree-core/neutree-core.go` → `cmd/neutree-core/app/builder.go`.

## Controller Pattern

Every resource is reconciled by a controller that follows three invariants:

- **Idempotent** — controllers must be safe to run repeatedly. A second pass on the same inputs must not cause extra side effects (no double-create, no duplicate cleanup).
- **Spec/Status separation** — users (and `neutree-api`) write only `Spec`; `Status` is owned by the controller and never mutated by clients. Reviewers should reject any patch that writes to `Status` from outside `controllers/`.
- **Soft delete** — resources are first marked with `deletion_timestamp`, the controller runs cleanup, then the row is permanently removed. Synchronous `DELETE` from the API path is not used.

`controllers/base_controller.go` provides the shared scaffolding that every controller embeds.

## Controller Surface

`controllers/` holds one controller per top-level resource type plus the shared base. Controllers fall into three categories by what they drive:

- **Cluster lifecycle** — provisions and tears down clusters (delegating to orchestrators in `internal/orchestrator/`).
- **Endpoint orchestration** — translates `Endpoint` / `ExternalEndpoint` specs into K8s Deployments or Ray Applications.
- **Catalog / supporting** — engines, image registries, model registries, model catalogs, and the RBAC trio (workspaces, roles, role assignments) plus user profiles and API keys.

The list of controllers in `controllers/*_controller.go` is the source of truth; the documentation deliberately does not enumerate them to avoid drift.

## Reconcile Cadence

`BaseController` runs a fixed pool of `workers` goroutines that pull keys off a `workqueue.RateLimitingInterface` (default exponential backoff). On top of the queue-driven path, a periodic `reconcileAll` re-lists every object at `syncInterval` and re-enqueues — this catches drift caused by missed events or external mutation.

Each `Reconcile` call goes through `beforeReconcileHooks` → the controller's own `Reconcile(obj)` → `afterReconcileHooks`. Hooks attach cross-cutting concerns (metrics, audit) without modifying business logic; new hooks register via `WithBeforeReconcileHook` / `WithAfterReconcileHook`.

A reconcile that returns an error stays on the queue and re-runs with backoff. A reconcile that returns nil but is not yet at the desired state should write progress to `Status` so the next periodic pass picks up where it left off — this is how long-running operations (cluster provisioning, endpoint rollout) make progress without blocking workers.

## Cluster Modes

Two deployment topologies share the same `Cluster` resource API, selected via `spec.config`. The dispatch happens inside `neutree-core`'s controllers and orchestrators:

1. **Kubernetes mode** — cluster lifecycle is delegated to the native Kubernetes control plane. Neutree installs only the inference infrastructure (router, metrics collector) in a dedicated namespace. Endpoints run as native Kubernetes Deployments. Code paths: `internal/orchestrator/kubernetes_orchestrator*.go`, `internal/cluster/kubernetes_cluster.go`.
2. **Static-node mode** — Ray over SSH provisions the cluster (each Ray node runs inside a Docker container). Endpoints deploy as Ray Applications on top of Ray Serve. Code paths: `internal/orchestrator/ray_orchestrator.go`, `internal/cluster/ray_ssh_cluster.go` + `ray_ssh_operation.go`.

Resources whose reconcile path depends on cluster mode (chiefly `Endpoint` and `Cluster` itself) delegate to an orchestrator interface in `internal/orchestrator/`, with one implementation per mode. Endpoint-level changes that touch one mode usually need a parallel change in the other; reviewers must verify both paths during code review.

This is independent from the **CP install mode** (how `neutree-api` / `neutree-core` themselves are deployed) — see [`architecture-neutree-cli.md#deploy-mode`](architecture-neutree-cli.md#deploy-mode). The two axes happen to share "K8s vs SSH" terminology but mean different things.

## Vendor Plugin Pairs

`internal/accelerator/plugin/gpu.go` (NVIDIA) and `internal/accelerator/plugin/amd_gpu.go` implement the same accelerator interface with vendor-specific details. The orchestrators consume this interface to discover and request GPU resources during reconcile. A change to one vendor implementation must be evaluated against:

- The other vendor implementation.
- `internal/accelerator/plugin/client.go` and `plugin.go` — the shared interface layer that frequently needs updates when vendor-specific logic changes.

## Adding a New Controller

Follow [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type) — the controller is wired through `cmd/neutree-core/app/builder.go` and its tests live alongside the controller file. Don't also wire the controller into `neutree-api` (that's the proxies' job, not the reconciler's).
