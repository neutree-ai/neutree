# Playbooks

> Step-by-step checklists for high-frequency additions. Every step is load-bearing — skipping one typically produces a working build that fails at runtime.
>
> Each playbook ends with a **Verify** section. Don't claim done until those commands pass.

## Adding a New Version to an Existing Engine

Scope: registering a new version (e.g. `vllm v0.18.0`) for an engine that already exists in `internal/engine/builtin.go`. Adding a brand-new engine type (Dockerfile, Makefile target, CI workflow choice) is out of scope here — use the `generate-engine-version-sglang` skill as a worked example.

### Naming conventions (read first — silent footgun)

The two trees use different conventions. Mismatching them produces a build that runs but silently drops SSH paths.

| Tree | Engine name | Version |
|------|-------------|---------|
| `cluster-image-builder/serve/<engine>/<version>/` | underscores (`llama_cpp`, `vllm`) | underscores (`v0_11_2`) — Python package style |
| `internal/engine/<engine>/<version>/` | hyphens (`llama-cpp`, `vllm`) | dots (`v0.11.2`) — embed-path style |

`cluster-image-builder/Makefile` translates between the two via `ENGINE_<NAME>_DIR_VERSION = $(shell ... tr '.' '_')`. If you hand-edit a version directory in kebab-case the Dockerfile build arg silently resolves to a non-existent path and the SSH image ends up missing the serve app.

### Steps

1. **Generate the schema first.** Run the schema generator (`scripts/gen-<engine>-schema.py` if one exists for this engine) — do **not** hand-edit. Hand-edited schemas have repeatedly drifted from upstream parameter names.

2. **Add the engine schema:** `internal/engine/<engine>/<version>/schema.json` (hyphen-engine, dot-version).

3. **Add the K8s deploy template:** `internal/engine/<engine>/<version>/templates/kubernetes/default.yaml`.
   - The path must match the `//go:embed` directive you'll add in step 5 character-for-character. A typo here surfaces only at compile time of step 5.
   - SSH mode does not consume this template — Ray Serve runs the app directly from the image. See [`architecture.md`](architecture.md) for the dual-orchestration split.

4. **Create the serve app:** `cluster-image-builder/serve/<engine>/<version>/app.py` (underscore-engine, underscore-version). The directory name must match the value `ENGINE_<NAME>_DIR_VERSION` resolves to in the Makefile — verify with `make -C cluster-image-builder docker-build-engine-<engine> ENGINE_<NAME>_VERSION=v0.X.Y -n` before committing.

5. **Wire schema parsing:** `internal/engine/schema.go`
   - Add `//go:embed <engine>/<version>/schema.json` and a `Get<Engine><Version>EngineSchema()` function.
   - Add the new entry to the `EngineSchemas` map. **Both edits are required.** A missing map entry compiles but fails with `engine schema not found` at runtime.

6. **Wire the deploy template:** `internal/engine/template.go`
   - Add `//go:embed <engine>/<version>/templates/kubernetes/default.yaml` and a `Get<Engine><Version>DeployTemplate()` function.
   - Add the new entry to the `DeployTemplates` map. Same compile-vs-runtime asymmetry as step 5.

7. **Register in builtins:** `internal/engine/builtin.go` + `builtin_test.go`
   - Append a `*v1.EngineVersion` entry under the existing engine's `Spec.Versions`.
   - **Images map — `ssh_<accelerator>` is optional.** `GetImageForSSHAccelerator` (see `api/v1/engine_types.go`) first looks for `ssh_<accelerator>` and falls back to the plain `<accelerator>` key. Only add an `ssh_<accelerator>` entry when SSH mode needs a different image than K8s (historically: K8s used the upstream `vllm/vllm-openai` while SSH needed a Ray-bundled `neutree/engine-vllm`). If a single image works for both modes — as in `vllm v0.17.1` — register the plain accelerator key only.
   - `DeployTemplate` map for `kubernetes/default` must reference the getter from step 6.
   - Update `builtin_test.go` to assert the new version is reachable via `GetEngineSchema("<engine>-<version>")` and `GetDeployTemplate("<engine>-<version>")` — this is what catches map-vs-getter drift from steps 5/6.

8. **Verify build outputs.** Run the Makefile target locally for both image variants the engine supports:
   ```bash
   cd cluster-image-builder
   make docker-build-engine-<engine> ENGINE_<NAME>_VERSION=v0.X.Y
   ```
   No Dockerfile edits should be needed — adding a version to an existing engine is parameterized via build args. If the build wants something new, you've crossed into "new engine type" territory and this playbook no longer applies.

9. **Add E2E coverage.** Append the new version to the engine matrix in the relevant `tests/e2e/endpoint_*` specs. This is mandatory, not optional — without an E2E entry, downstream upgrades will quietly skip the new version. See [`testing.md#e2e`](testing.md) for label conventions.

### Verify

```bash
make pre-commit                                   # gofmt / vet / boundaries / migration-pairs / lint
go test ./internal/engine/...                     # builtin_test must include the new version
make -C cluster-image-builder docker-build-engine-<engine> ENGINE_<NAME>_VERSION=v0.X.Y
```

Then on a real cluster: deploy a model with the new engine/version pair and confirm the endpoint reaches `Running` in **both** k8s and SSH modes.

---

## Adding a New Resource Type

Scope: a new top-level resource (e.g. `Foo`) reachable through the CLI, persisted in Postgres with workspace-isolated RLS, and reconciled by a controller.

Every step is required — a missed wiring step usually produces a resource that compiles and migrates but cannot be created through the CLI or reached through the gateway.

### Steps

1. **Define the API type:** `api/v1/<resource>_types.go` + `_types_test.go` — both `Foo` and `FooList` structs.

2. **Register the type — two calls, both required:** `api/v1/register.go`
   - `SchemeBuilder.Register(&Foo{}, &FooList{}, ...)` — kind registration.
   - `SchemeBuilder.RegisterTable(map[string]string{"foos": "Foo", ...})` — table → kind mapping. Without this entry, the storage layer can't unmarshal rows into the new type.

3. **Add the storage table constant:** `pkg/storage/storage.go` — append `FOO_TABLE = "foos"`. The route handler in step 7 references this constant via `CreateStructProxyHandler[v1.Foo](deps, storage.FOO_TABLE)`.

4. **DB migrations (up + down):** `db/migrations/NNN_<description>.{up,down}.sql`
   - Table schema with workspace column.
   - Workspace-isolation RLS policies — see [`database.md#rls`](database.md).
   - Validation triggers if the resource has invariants.
   - **If the resource needs reconciliation:** consider the `observed_spec_hash` pattern (see migration `055`).
   - **If listings need stable ordering:** consider the `status_sort_priority` pattern (see migration `056`).

5. **RBAC seed migration:** a separate migration granting workspace roles permission on the new table. Without this, the resource exists but default workspace members cannot see or create rows. See migrations `057` and `059` for the pattern. Skipping this step produces a "creates from admin, invisible to users" bug class.

6. **DB integration tests:** `db/dbtest/`
   - Tests are organized **by theme**, not per-resource. Add cases to existing files where they fit:
     - RLS isolation cases → an existing RLS file or `basic_test.go`.
     - Validation triggers → `validation_test.go`.
     - Soft-delete behavior → `soft_delete_test.go`.
     - RBAC matrix → `user_profile_rbac_test.go`.
   - Only add a new file if the resource introduces a genuinely new theme.

7. **Route handler:** `internal/routes/proxies/<resource>_proxy.go` + `_test.go`
   - Export `RegisterFooRoutes(group, middlewares, deps)` following the existing engine/cluster pattern.
   - **Backfill middleware caveat:** if the schema includes oneOf / discriminated-union / type-switch fields, audit `handlePatchWithBackfill` before relying on PATCH — it has injected empty sibling maps in the past.

8. **Wire the route into the API builder:** `cmd/neutree-api/app/builder.go`
   - Append `"rest/foos": ProxiesRouteFactory(proxies.RegisterFooRoutes)` to the route map.
   - `factory.go` only needs editing if the new resource needs dependencies beyond the standard `proxies.Dependencies` — usually no edit required.

9. **Controller:** `controllers/<resource>_controller.go` + `_controller_test.go`
   - Follow the `engine_controller.go` / `cluster_controller.go` shape.
   - Wire it into the **core** runtime: `cmd/neutree-core/app/builder.go` and `factory.go`. Do not also wire it into `neutree-api` — API hosts proxies, core hosts controllers. Mixing them is the most common wiring mistake.

10. **Gateway integration (conditional):** `internal/gateway/kong.go`
    - **Only if the resource sits on the inference request path** (current set: `endpoint`, `external_endpoint`, `api_key`). For everything else, skip this step.
    - If a new Kong plugin is needed: `gateway/kong/plugins/<plugin>/`.

11. **CLI commands:** every resource-aware verb subcommand needs a branch for the new resource.
    - `cmd/neutree-cli/app/cmd/apply/apply.go`
    - `cmd/neutree-cli/app/cmd/get/get.go`
    - `cmd/neutree-cli/app/cmd/delete/delete.go`
    - `cmd/neutree-cli/app/cmd/wait/wait.go`
    - "Add CLI commands" as one bullet has historically led to half-wired CLIs (resource visible to `apply` but not `get`).

12. **E2E coverage:** `tests/e2e/<resource>_test.go` with an independent Ginkgo label so the suite can target it. See [`testing.md#e2e`](testing.md) for label conventions and the `endpoint && lifecycle` timeout caveat.

### Verify

```bash
make pre-commit                                   # gofmt / vet / boundaries / migration-pairs / lint
go test ./api/v1/... ./pkg/storage/... ./internal/routes/proxies/... ./controllers/...
make dbtest                                       # RLS + RBAC seed migration must both pass
```

Then end-to-end: `neutree-cli apply -f sample-foo.yaml` → `neutree-cli get foo` → reconciler reaches a terminal status → `neutree-cli delete foo` removes the row. Run the full happy path in **both** k8s and SSH cluster modes if the resource interacts with cluster orchestration.
