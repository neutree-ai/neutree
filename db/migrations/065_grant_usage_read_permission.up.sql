-- Grant the workspace:usage-read permission (added in 064) to the built-in
-- roles. Separate migration because newly added enum values cannot be
-- referenced in the same transaction that adds them.

-- admin: refresh to every permission in the enum (picks up workspace:usage-read
-- automatically). Admin can therefore read usage across all workspaces.
SELECT api.update_admin_permissions();

-- workspace-user: intentionally NOT granted by default. Unlike the trace
-- permissions, usage-read is not part of the baseline workspace-user role; it is
-- granted on demand per workspace (in EE, via a workspace-scoped role
-- assignment carrying this permission). Leaving update_workspace_user_permissions
-- untouched keeps the default workspace-user set unchanged.
