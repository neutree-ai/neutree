# Database & Auth

> PostgreSQL schema, RLS policies, migration workflow, and authentication / authorization in one place. Read this before touching DB or auth code.

## PostgreSQL + PostgREST Model

- **Single source of truth**: the database schema *is* the API. PostgREST derives the REST surface automatically from the schema.
- **RLS (row-level security)**: authorization is pushed down into the database. PostgREST forwards the JWT as a session variable, and policies key off it.
- **Validation triggers**: field constraints, referential checks, and state machines live as triggers that raise errors with actionable hints.

## Migration Rules

- Location: `db/migrations/`.
- Naming: `NNN_<description>.up.sql` + `NNN_<description>.down.sql`.
- Current highest number: **059** — start new migrations at 060.
- Any migration that touches RLS or permissions must ship with an integration test under `db/dbtest/`.

> ⚠️ **Pair rule (new migrations)**: every new migration must ship `.up.sql` and `.down.sql` together; a missing `.down.sql` breaks rollback. The pre-commit hook (P0-2) enforces this.
>
> **Historical exception**: migrations `001_rbac`, `002_user-extend`, `003_resources`, and `005_kong` are bootstrap SQL without rollback files. These pre-date the rule and are grandfathered in; the hook ignores them.

### Migration skeleton

```sql
-- NNN_add_xxx.up.sql
BEGIN;

ALTER TABLE api.resource
  ADD COLUMN new_field TEXT NOT NULL DEFAULT '';

-- RLS policy (isolate by workspace)
CREATE POLICY resource_workspace_isolation ON api.resource
  FOR ALL
  USING (workspace_id = current_setting('request.jwt.claims', true)::json->>'workspace_id');

-- Validation trigger (returns a hint so agents/users know how to fix)
CREATE FUNCTION api.validate_resource() RETURNS trigger AS $$
BEGIN
  IF NEW.new_field = '' THEN
    RAISE EXCEPTION USING
      ERRCODE = 'P0001',
      MESSAGE = 'new_field cannot be empty',
      HINT = 'Set new_field to a non-empty value';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

COMMIT;
```

```sql
-- NNN_add_xxx.down.sql
BEGIN;
DROP POLICY IF EXISTS resource_workspace_isolation ON api.resource;
DROP FUNCTION IF EXISTS api.validate_resource();
ALTER TABLE api.resource DROP COLUMN IF EXISTS new_field;
COMMIT;
```

## Testing

- Unit tests: no real DB; use the mocks generated under `pkg/storage/mocks`.
- Integration tests: `db/dbtest/` spins up a real PostgreSQL + PostgREST + GoTrue.
- Command: `make db-test`.

## Row-Level Security

Authorization is enforced inside the database. Every protected resource table needs RLS policies that combine two checks:

- **Workspace isolation** — the row's `workspace_id` must match the JWT claim.
- **Permission check** — `has_permission(user, action, workspace)` validates the user's role + action against the `roles` and `role_assignments` tables.

### Policy pattern

```sql
CREATE POLICY resource_read ON api.resource
  FOR SELECT
  USING (
    has_permission(
      auth.uid(),
      'resource:read',
      (metadata).workspace
    )
  );
```

`has_permission()` is defined in `db/migrations/001_rbac.up.sql`. JWT issuance and API-key-to-JWT exchange are HTTP middleware concerns and live in `internal/middleware/auth.go` — out of scope for this file.

## Adding a New Resource Type

The full 11-step checklist lives in [`playbooks.md#adding-a-new-resource-type`](playbooks.md#adding-a-new-resource-type). This file focuses on the database-layer pieces (steps 3 and 4 in the checklist).

## Common Errors and Fixes

| Symptom | Root cause | Fix |
|---------|------------|-----|
| PostgREST returns 401 | JWT expired or claims missing | Inspect token refresh in `internal/auth/` |
| PostgREST returns `[]` when data exists | RLS policy doesn't cover the current workspace | Add `USING (workspace_id = ...)` in the migration |
| Migration passes locally, fails in CI | Depends on state not created by this migration | Split into self-contained sequential migrations |
| `db-test` hangs on startup | Stale docker-compose volumes | `make db-test-clean` |

## Files to know

- `db/docker-compose.test.yml` — test-environment compose file.
- `db/migrations/001_rbac.up.sql` — initial schema (start reading here).
- `internal/middleware/auth.go` — JWT verification and API-key-to-JWT exchange.
- `internal/auth/` — GoTrue client (login, token issue/refresh).
- `pkg/storage/` — PostgREST client.
