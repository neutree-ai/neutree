# Playbooks

> Step-by-step checklists for high-frequency additions. Every step is load-bearing — skipping one typically produces a working build that fails at runtime.

## Adding a New Engine Version

1. Create the serve app: `cluster-image-builder/serve/<engine>/<version>/app.py`.
2. Add the engine schema: `internal/engine/<engine>/<version>/schema.json`.
3. Add the K8s deploy template: `internal/engine/<engine>/<version>/templates/kubernetes/default.yaml`.
4. Register in builtins: `internal/engine/builtin.go` + `builtin_test.go`.
5. Update schema parsing: `internal/engine/schema.go` + `schema_test.go`.
6. Update the template loader: `internal/engine/template.go`.
7. Update the Dockerfile: `cluster-image-builder/Dockerfile.engine-<engine>`.
8. Update `cluster-image-builder/Makefile` with the new build target.
9. Add E2E test coverage where applicable.

## Adding a New Resource Type

Every step is required — a missed wiring step usually produces a resource that compiles and migrates but cannot be created through the CLI or reached through the gateway.

1. Define the API type: `api/v1/<resource>_types.go` + `_types_test.go`.
2. Register the type: `api/v1/register.go`.
3. Create DB migrations (up + down): `db/migrations/NNN_<description>.{up,down}.sql`, including table schema, workspace-isolation RLS, and any validation triggers.
4. Write the DB integration test: `db/dbtest/<resource>_test.go` — cover RLS isolation and the permission matrix.
5. Add the controller: `controllers/<resource>_controller.go` + `_controller_test.go`.
6. Add the route handler: `internal/routes/proxies/<resource>_proxy.go` + `_test.go`.
7. Wire into the API builder: `cmd/neutree-api/app/builder.go` and `factory.go`.
8. Wire into the core builder: `cmd/neutree-core/app/builder.go` and `factory.go`.
9. Update gateway integration: `internal/gateway/kong.go`, and add Kong plugin support under `gateway/kong/plugins/` if the resource participates in request routing.
10. Add CLI commands: `cmd/neutree-cli/app/cmd/<resource>/...`.
11. Add E2E coverage: `tests/e2e/<resource>_test.go` with an independent Ginkgo label.
