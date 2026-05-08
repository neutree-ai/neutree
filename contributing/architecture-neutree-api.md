# neutree-api

## Process Role

`neutree-api` is one of two independent control-plane processes (the other is `neutree-core`). It serves HTTP CRUD on top of PostgREST and is the single entry point for UI / CLI / external clients.

It does **not**:
- run controllers or reconcile loops (that's `neutree-core`),
- talk to `neutree-core` directly (both processes connect to PostgREST independently — see [`architecture.md#data-flow`](architecture.md#data-flow)),
- handle inference traffic (Kong fronts inference; `neutree-api` is for control-plane CRUD only).

Entry point: `cmd/neutree-api/neutree-api.go` → `cmd/neutree-api/app/builder.go`.

## Route Topology

Routes are registered by name into a map in `cmd/neutree-api/app/builder.go`. The standard pattern is one resource → one `internal/routes/proxies/<resource>_proxy.go` file → one `RegisterFooRoutes` function → one map entry under `rest/<plural-resource>`.

Two route categories:

**REST resource proxies** (`rest/<resource>`) — thin proxies over PostgREST that apply field masking, auth, and method allow-listing per resource. Implementation: `CreateStructProxyHandler[v1.Foo]` from `internal/routes/proxies/`. Method allow-listing is intentional — most resource proxies expose only `GET / POST / PATCH`. `PUT` is rejected (use `PATCH`); `DELETE` is rejected because deletion goes through the `deletion_timestamp` soft-delete pattern (see [`architecture-neutree-core.md#controller-pattern`](architecture-neutree-core.md#controller-pattern)). Adding a new resource means adding a `<resource>_proxy.go` and one entry to the route map; see [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type).

**Specialized routes** — non-REST endpoints that cannot be generic PostgREST proxies:

| Path prefix | Purpose | Handler |
|-------------|---------|---------|
| `/serve-proxy/:workspace/:name/*path` | Reverse-proxy to a Ray Serve application running on the cluster | `RegisterRayServeProxyRoutes` |
| `/dashboard-proxy/:workspace/:name/*path` | Reverse-proxy to a Ray dashboard | `RegisterRayDashboardProxyRoutes` |
| `/k8s-proxy/:workspace/:name/*path` | Authenticated reverse-proxy to a cluster's Kubernetes API server | `RegisterKubernetesProxyRoutes` |
| `/endpoint-logs/...` | Endpoint log streaming | `RegisterEndpointLogsRoutes` |
| `/auth/...` | GoTrue token issue/refresh | `RegisterAuthRoutes` |
| `/credentials/...` | Image registry / model registry credential access | `RegisterCredentialsRoutes` |
| `/system/...` | Health, version, system info | `RegisterSystemRoutes` |
| `/models/...` | OpenAI-compatible model listing | `RegisterModelsRoutes` |
| `/clusters/...` | Cluster operations beyond raw PostgREST CRUD | `RegisterClusterRoutes` |
| `/rest/rpc/:path` | PostgREST RPC passthrough | `RegisterPostgrestRPCProxyRoutes` |

## Authentication

All routes except `/auth/*` go through a single `auth` middleware (`internal/middleware/auth.go`), wired in `cmd/neutree-api/app/builder.go` via the `defaultRoutesToMiddlewares` map. The middleware accepts two credential forms on the `Authorization` header:

1. **GoTrue JWT** — issued by `/auth/login`, validated against `JwtSecret` from `AuthConfig`. Used by UI sessions.
2. **API key** (self-contained, AES-encrypted) — issued by `/rest/api-keys`, decrypted and converted to a PostgREST-compatible JWT in-flight so downstream PostgREST sees a uniform token. Used by CLI and external integrations.

Per-resource access checks (workspace membership, RBAC) are not in `auth` — they are enforced by PostgREST's RLS policies on the database side (see [`database.md#rls`](database.md#rls)).

Two more middlewares exist in `internal/middleware/` but are **not** in the default chain: `permission.go` (fine-grained method/resource gating) and `deletion_validation.go`. They are opt-in per route group and used selectively.

## Adding a New Route

For a new REST resource: follow [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type) — the route handler is one of the wiring steps.

For a new specialized route:
1. Add the handler under `internal/routes/<group>/`.
2. Export a `Register<Group>Routes(group, middlewares, deps)` function.
3. Add an entry to the `defaultRouteInits` map in `cmd/neutree-api/app/builder.go`.
4. Add the route to `defaultRoutesToMiddlewares` so the `auth` middleware actually applies (forgetting this exposes the route unauthenticated).
5. If the route needs new dependencies beyond `proxies.Dependencies`, add a route factory type in `cmd/neutree-api/app/factory.go`.
