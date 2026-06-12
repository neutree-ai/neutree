-- Revert the quota feature (NEUTREE-GENERAL-9). Enum values added in 064 remain
-- (PostgreSQL cannot drop enum values); workspace-user permissions are restored
-- to the pre-quota set.
DROP FUNCTION IF EXISTS api.get_all_api_keys_remaining();
DROP FUNCTION IF EXISTS api.get_api_key_remaining(UUID);
DROP FUNCTION IF EXISTS api.quota_period_usage(UUID[], TEXT, DATE);
DROP FUNCTION IF EXISTS api.set_quota_policy(TEXT, TEXT, BIGINT, TEXT, UUID, UUID);
DROP FUNCTION IF EXISTS api.quota_period_start(TEXT, DATE);
DROP TRIGGER IF EXISTS validate_quota_hierarchy_trigger ON api.quota_policies;
DROP FUNCTION IF EXISTS api.validate_quota_hierarchy();
DROP TABLE IF EXISTS api.quota_policies;

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

SELECT api.update_workspace_user_permissions();

NOTIFY pgrst, 'reload schema';
