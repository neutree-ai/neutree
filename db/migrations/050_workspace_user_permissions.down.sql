-- Remove workspace-user permissions (reset to empty)
UPDATE api.roles
SET spec = ROW((spec).preset_key, ARRAY[]::api.permission_action[])::api.role_spec
WHERE (metadata).name = 'workspace-user';

-- Drop the function
DROP FUNCTION IF EXISTS api.update_workspace_user_permissions();
