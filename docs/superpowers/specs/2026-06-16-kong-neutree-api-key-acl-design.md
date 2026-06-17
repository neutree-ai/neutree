# Kong and Neutree API Key Authorization Design

## Context

NEU-167 tracks a gateway authorization gap: any valid Neutree API key can
currently call any inference endpoint route in Kong, including IE
(`/workspace/{workspace}/endpoint/{name}`) and EE
(`/workspace/{workspace}/external-endpoint/{name}`), regardless of whether the
API key owner has access to the target workspace or resource.

The current chain is:

1. Neutree API keys are self-contained `sk_` values created by
   `api.create_api_key`.
2. `ApiKeyController` syncs each key to Kong as a Consumer with
   `consumer.custom_id = api_key_id` and a `key-auth` credential.
3. Kong rewrites `Authorization: Bearer <sk_...>` or `x-api-key` into
   `kong_apikey`, then `key-auth` validates that the key exists.
4. The `neutree-ai-gateway` plugin handles protocol conversion, upstream
   selection, usage metadata, and trace metadata.
5. There is no route-level authorization check tying the authenticated
   `api_key_id` to the target workspace and IE/EE.

The fix must keep inference traffic independent from `neutree-api` request-time
availability. Neutree should compute permissions and push them into Kong; Kong
should enforce the decision locally.

## Repo Standards Read

- `CLAUDE.md`: source work stays in the task worktree and ignored generated
  paths are not read or edited.
- `contributing/architecture.md`: inference traffic flows through Kong and does
  not call `neutree-api` or `neutree-core` on the request path.
- `contributing/architecture-neutree-api.md`: API-key authentication for
  control-plane REST requests is middleware/PostgREST oriented and should not be
  mixed into inference-route authorization.
- `contributing/architecture-neutree-core.md`: controller behavior must remain
  idempotent and tolerate periodic reconcile retries.
- `contributing/testing.md`: Go tests stay package-local, DB permission changes
  need `db/dbtest` coverage, and E2E uses Ginkgo labels with profile-driven
  environment data.
- `contributing/coding-standards.md`: changes must keep imports organized,
  avoid oversized helpers, and pass scoped lint/vet gates.
- `contributing/database.md`: RBAC remains database-backed through
  `has_permission`; this change does not add a migration.

## Minimal Change Boundary

This design changes only Kong gateway synchronization and the verification
surface needed to prove route-level API-key authorization:

- In scope: Neutree-managed ACL group naming, API-key Consumer ACL membership
  sync, IE/EE route ACL plugins, Kong custom plugin priority, unit tests, DB
  permission regression coverage, and Step 5 E2E acceptance design.
- Out of scope: a request-time call from Kong to Neutree, a new custom Kong
  authorization plugin, API-key fixed scopes, new database schema, new
  permissions, CLI/API wire-shape changes, and UI changes.
- Existing non-Neutree Kong ACL groups are preserved; only groups with the
  `nt:` prefix are managed by this feature.

## Decisions

- Use Kong's native `acl` plugin for gateway authorization.
- Keep Kong `key-auth` for API key authentication.
- Do not add a custom Neutree authorization plugin for the first version.
- Reuse existing permissions:
  - IE inference requires `endpoint:read`.
  - EE inference requires `external_endpoint:read`.
- API keys are limited to their own `metadata.workspace`. A key created in
  workspace A must not call workspace B even if the owner has permissions in
  workspace B.
- Authorization is exact to `workspace + endpoint_type + endpoint_name`.
- Permission changes are eventually consistent at minute-level latency through
  periodic reconcile.
- Gateway authorization is default-deny: if a Consumer is not in the route's
  allowed ACL group, Kong returns `403`.

## Architecture

Kong executes inference requests in this order:

1. The existing pre-function rewrites client API key headers into
   `kong_apikey`.
2. `key-auth` authenticates the API key and resolves the Consumer.
3. `acl` checks whether the Consumer belongs to the route's allowed group.
4. `neutree-ai-gateway` runs protocol conversion, model routing, usage, and
   trace handling.
5. The request reaches the upstream endpoint.

This order is a hard requirement. If Kong's default plugin priorities would run
`neutree-ai-gateway` before `acl`, the implementation must either lower the
custom plugin priority or configure plugin ordering so ACL enforcement happens
first.

Each IE/EE route gets exactly one Neutree-managed ACL group. The logical
identity is `workspace + endpoint_type + resource_name`, and the concrete Kong
group uses a stable hash so workspace and resource names do not leak into Kong
group names or violate Kong naming constraints:

- IE: `nt:endpoint:{sha256(workspace, endpoint, endpoint_name)}`
- EE: `nt:external-endpoint:{sha256(workspace, external-endpoint, external_endpoint_name)}`

Neutree core periodically computes expected Consumer ACL membership. For each
active API key, it reads the key owner, key workspace, and the owner's current
RBAC permissions in that workspace. It then joins the Kong Consumer to the IE/EE
ACL groups the key is allowed to call.

## Components

### Route ACL Configuration

`SyncEndpoint` and `SyncExternalEndpoint` continue to create or update Kong
Service and Route objects. After the Route exists, they also ensure that the
Route has a Kong `acl` plugin configured with one `allow` group matching the
target resource.

The Route ACL plugin must be synced before the route is considered healthy.
Routes without their expected ACL plugin are treated as gateway sync failures,
not as public routes.

Deleting an IE or EE deletes its Kong Route. The next API key reconcile removes
stale Neutree ACL memberships that reference the deleted resource.

### Consumer ACL Membership Sync

`SyncAPIKey` keeps the current Consumer and `key-auth` sync behavior. It adds an
ACL sync step:

1. Build desired `nt:` ACL groups for the API key.
2. List the Consumer's current ACL groups from Kong.
3. Create missing desired groups.
4. Delete stale Neutree-managed groups.
5. Leave non-Neutree groups untouched.

Only ACL groups with the `nt:` prefix are managed by Neutree. This avoids
removing manual Kong ACL configuration or configuration owned by another
component.

### Periodic Reconcile

API key access is affected by API keys, roles, role assignments, workspaces, IE,
and EE. Instead of wiring a hard trigger from every related resource, the first
version uses periodic API key reconciliation to recompute desired ACL groups for
each active key.

This satisfies the agreed minute-level consistency goal and keeps the design
simple. Failed Kong sync attempts are retried in later reconcile loops.

## Authorization Algorithm

For API key `K`:

1. Read `K.user_id` and `K.metadata.workspace`.
2. If `K` is deleted or its Kong Consumer is missing, ensure no usable Kong
   credential remains.
3. Check `has_permission(K.user_id, 'endpoint:read', K.metadata.workspace)`.
   If true, add all active IE groups in `K.metadata.workspace`.
4. Check
   `has_permission(K.user_id, 'external_endpoint:read', K.metadata.workspace)`.
   If true, add all active EE groups in `K.metadata.workspace`.
5. Diff desired groups against the Consumer's current `nt:` ACL groups.
6. Apply create/delete operations through Kong Admin API.

No resources outside `K.metadata.workspace` are included, even if the user has
RBAC permissions in those workspaces.

## Data Flow

### Inference Request

1. Client calls:
   - `/workspace/{workspace}/endpoint/{name}/...`, or
   - `/workspace/{workspace}/external-endpoint/{name}/...`
2. Kong authenticates the API key through `key-auth`.
3. Kong ACL checks the Consumer's group membership against the route allow
   group.
4. If the group is missing, Kong returns `403`.
5. If the group is present, `neutree-ai-gateway` processes the request.
6. Existing usage and trace logging continue to use `consumer.custom_id` as
   `api_key_id` and the request path as the target workspace/resource.

### ACL Sync

1. Neutree core lists active API keys.
2. For each key, it computes desired groups from current RBAC and key workspace.
3. Neutree core reads current Kong ACL groups for the key Consumer.
4. It creates missing desired groups and deletes stale `nt:` groups.
5. Sync errors are recorded on API key status and retried later.

## Error Handling

- Missing ACL membership: Kong returns `403`.
- API key has no desired groups: all IE/EE inference calls are denied.
- Route sync fails to create ACL plugin: mark the resource sync as failed and
  do not report a healthy gateway route.
- API key deletion: delete the Kong Consumer, removing credentials and ACL
  memberships.
- IE/EE deletion: delete the Kong Route. ACL membership cleanup happens in the
  next API key reconcile.
- Role or membership removal: stale access can remain until the next successful
  reconcile, bounded by the minute-level consistency target.
- Kong Admin API failure: keep retrying through reconcile. Existing Kong state
  may remain temporarily active until the next successful sync.
- Unknown or malformed route: Kong route matching and ACL configuration deny
  access by default because there is no matching allowed group.

## Naming

Neutree ACL groups are logically keyed by `workspace + endpoint_type +
resource_name`, but the concrete Kong group name is hashed:

```text
nt:{endpoint_type}:{sha256(workspace, endpoint_type, resource_name)}
```

Implementation centralizes group name generation so workspace or resource names
with Kong-unsafe characters never appear in cleartext. Tests cover special
characters and length boundaries.

## Rejected Alternatives

### Kong Calls Neutree API on Every Request

This keeps authorization logic centralized but makes every inference request
depend on `neutree-api` and database availability. It also adds latency to the
hot path. This was rejected because Kong should enforce local authorization from
Neutree-pushed state.

### Custom Neutree Authorization Plugin

A custom plugin could read a Neutree-specific authorization table from plugin
configuration. This gives full control but duplicates Kong ACL behavior and
adds more custom Lua code to maintain. Kong ACL already supports the needed
Consumer group check.

### API Key Fixed Scope Only

Creating keys with fixed scopes is simple, but it does not naturally respond to
RBAC changes. The selected design preserves current RBAC semantics and uses
periodic sync to update Kong.

## Testing Plan

### Pre-development Manual E2E Baseline

Before developing automated E2E coverage, run a manual baseline against the
current behavior:

1. Use an API key from workspace A to call an IE and EE in workspace A.
2. Use the same key to call an IE and EE in workspace B.
3. Record current Kong responses, usage behavior, trace behavior, and reconcile
   status.

This baseline is for reproducing NEU-167 and validating the test environment. It
does not replace automated E2E acceptance.

### Unit Test

- ACL group name generation for IE, EE, special characters, and length limits.
- Desired group calculation for:
  - user has `endpoint:read`;
  - user lacks `endpoint:read`;
  - user has `external_endpoint:read`;
  - user lacks `external_endpoint:read`;
  - API key workspace restriction.
- Kong ACL diff:
  - create missing groups;
  - delete stale `nt:` groups;
  - preserve non-Neutree groups.
- Route plugin sync adds ACL plugin with the expected allow group.
- Plugin execution order ensures ACL runs before `neutree-ai-gateway` behavior
  that can return or rewrite requests.

### DB Test

- `has_permission(user, 'endpoint:read', workspace)` resolves correctly for
  workspace-scoped role assignments.
- `has_permission(user, 'external_endpoint:read', workspace)` resolves
  correctly for workspace-scoped role assignments.
- Role or role-assignment removal is visible to the ACL computation layer.
- Controller/service-role access can read API key fields needed for sync
  without widening end-user API key RLS.

### E2E Test

- API key from workspace A can call an authorized IE in workspace A.
- API key from workspace A can call an authorized EE in workspace A.
- API key from workspace A calling workspace B IE returns `403`.
- API key from workspace A calling workspace B EE returns `403`.
- After permission removal and reconcile, the same key receives `403`.
- OpenAI chat, Anthropic messages, and model list routes all require ACL.
- Successful usage and trace records still include `api_key_id`, target
  workspace, target endpoint type, and target endpoint name.
- Rejected cross-workspace calls do not create successful usage records.

### E2E Classification

Every case keeps a manual Step 5 execution requirement. Code-backed E2E is an
additional layer for the deterministic happy path, not a replacement for manual
verification.

| Case | Classification | Code E2E decision |
|---|---|---|
| Authorized same-workspace EE model list and OpenAI chat | Manual Step 5 + Code E2E | Reuse `external-endpoint && openai` |
| Authorized same-workspace EE Anthropic messages | Manual Step 5 only | Existing suite covers protocol behavior; ACL proof is manual |
| Authorized same-workspace IE chat on a Running endpoint | Manual Step 5 only | Depends on cluster/model capacity |
| Cross-workspace EE denied with `403` | Manual Step 5 only | Requires multi-workspace environment |
| Cross-workspace IE denied with `403` | Manual Step 5 only | Requires multi-workspace plus Running IE |
| Permission removal followed by reconcile denies access | Manual Step 5 only | Requires non-admin role mutation and reconcile observation |
| Direct Kong ACL membership removal denies access | Manual Step 5 only | Route default-deny proof |
| Successful usage/trace records still include API-key and target metadata | Manual Step 5 only | Requires usage/trace query access |
| Rejected calls do not create successful usage records | Manual Step 5 only | Requires usage query access |

### E2E Environment Capability Matrix

| Capability | Required by | Current `10.255.1.54` evidence | Status |
|---|---|---|---|
| EE with mock upstream | EE allow/deny happy path | Created temporary EE and upstream mock | Available |
| Running IE | IE allow/deny path | Environment listed zero clusters and endpoints | Missing |
| Two workspaces | Cross-workspace denial | Current DB migration allows one workspace | Missing |
| Non-admin role assignment and permission revoke | Permission-removal reconcile | Shared environment only validated admin/global path | Missing |
| Kong Admin access | Route ACL and membership evidence | Kong Admin reachable through container network | Available |
| Usage/trace query visibility | Usage/trace assertions | Existing scoped E2E does not assert these fields | Missing |

Step 5 cannot be considered complete until the missing capabilities are supplied
and the corresponding manual cases pass, or the remaining risk is explicitly
accepted before Step 9.

## Rollout Notes

- Add ACL route configuration before enabling API key membership sync so routes
  become default-deny until memberships are present.
- Run the pre-development manual E2E baseline before changing behavior.
- Validate in a development environment with both IE and EE routes.
- Monitor Kong ACL group count and sync latency. If group count grows too high,
  revisit group naming or route-level grouping strategy.

## References

- NEU-167: `http://jira.smartx.com/browse/NEU-167`
- Kong ACL plugin: `https://developer.konghq.com/plugins/acl/`
- Kong Key Auth plugin: `https://developer.konghq.com/plugins/key-auth/`
- Existing Neutree Kong sync: `internal/gateway/kong.go`
- Existing API key controller: `controllers/api_key_controller.go`
- Existing RBAC docs: `docs/rbac.md`
