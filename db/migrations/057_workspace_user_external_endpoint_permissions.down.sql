-- Revert workspace-user to previous permissions (remove external_endpoint CUD, keep read)
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        array_remove(
            array_remove(
                (spec).permissions,
                'external_endpoint:create'::api.permission_action
            ),
            'external_endpoint:update'::api.permission_action
        ),
        'external_endpoint:delete'::api.permission_action
    )
)::api.role_spec
WHERE (metadata).name = 'workspace-user';

-- Restore original function without external_endpoint permissions
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
        'model_catalog:delete'
    ]::api.permission_action[];

    UPDATE api.roles
    SET spec = ROW((spec).preset_key, workspace_user_permissions)::api.role_spec
    WHERE (metadata).name = 'workspace-user';
END;
$$ LANGUAGE plpgsql;
