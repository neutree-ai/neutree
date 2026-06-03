-- Grant the trace-read permissions (added in 062) to the built-in roles.
-- Separate migration because newly added enum values cannot be referenced in
-- the same transaction that adds them.

-- admin: refresh to every permission in the enum (picks up the trace perms).
SELECT api.update_admin_permissions();

-- workspace-user: grant both endpoint:trace-read and external_endpoint:trace-read
-- so workspace members can read inference traces by default.
CREATE OR REPLACE FUNCTION api.update_workspace_user_permissions()
RETURNS VOID AS $$
DECLARE
    workspace_user_permissions api.permission_action[];
BEGIN
    workspace_user_permissions := ARRAY[
        'workspace:read',
        'endpoint:read',
        'endpoint:create',
        'endpoint:update',
        'endpoint:delete',
        'image_registry:read',
        'image_registry:create',
        'image_registry:update',
        'image_registry:delete',
        'model_registry:read',
        'model_registry:create',
        'model_registry:update',
        'model_registry:delete',
        'model:read',
        'model:push',
        'model:pull',
        'model:delete',
        'engine:read',
        'engine:create',
        'engine:update',
        'engine:delete',
        'cluster:read',
        'cluster:create',
        'cluster:update',
        'cluster:delete',
        'model_catalog:read',
        'model_catalog:create',
        'model_catalog:update',
        'model_catalog:delete',
        'external_endpoint:read',
        'external_endpoint:create',
        'external_endpoint:update',
        'external_endpoint:delete',
        'endpoint:trace-read',
        'external_endpoint:trace-read'
    ]::api.permission_action[];

    UPDATE api.roles
    SET spec = ROW((spec).preset_key, workspace_user_permissions)::api.role_spec
    WHERE (metadata).name = 'workspace-user';
END;
$$ LANGUAGE plpgsql;

-- Apply updated permissions
SELECT api.update_workspace_user_permissions();
