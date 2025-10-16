DROP TRIGGER IF EXISTS enforce_global_role_assignment_trigger ON api.role_assignments;
DROP FUNCTION IF EXISTS api.enforce_global_role_assignment();

DROP TRIGGER IF EXISTS validate_workspace_limit_on_insert ON api.workspaces;
DROP FUNCTION IF EXISTS api.validate_workspace_limit();
