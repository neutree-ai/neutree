-- ----------------------
-- Prevent deletion of default workspace
-- ----------------------
DROP TRIGGER IF EXISTS prevent_default_workspace_deletion_trigger ON api.workspaces;
DROP FUNCTION IF EXISTS api.prevent_default_workspace_deletion();