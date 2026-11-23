-- Reverts model_invoke: no role but admin and model-invoker holds
-- model:invoke; workspace-user and the workspace model listing as before.

DROP FUNCTION IF EXISTS api.api_key_owner_active(UUID);
DROP FUNCTION IF EXISTS api.list_invokable_models(TEXT, UUID);

CREATE OR REPLACE FUNCTION api.get_workspace_models(p_workspace TEXT)
RETURNS TABLE (model TEXT, source TEXT, endpoint_name TEXT, source_label TEXT)
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT DISTINCT
        (e.status).served_models[1] AS model,
        'endpoint'::text          AS source,
        (e.metadata).name::text   AS endpoint_name,
        'self-hosted'::text       AS source_label
    FROM api.endpoints e
    WHERE (e.metadata).workspace = p_workspace
      AND (e.metadata).deletion_timestamp IS NULL
      AND (e.status).served_models[1] IS NOT NULL
      AND trim((e.status).served_models[1]) <> ''
    UNION
    SELECT DISTINCT
        k::text                    AS model,
        'external_endpoint'::text  AS source,
        (ee.metadata).name::text   AS endpoint_name,
        -- An upstream with endpoint_ref is fronting an endpoint this platform
        -- runs, so the model is self-hosted without the admin having to say
        -- so. An explicit entry still wins.
        COALESCE(
            (ee.spec).model_sources ->> k,
            CASE WHEN u.endpoint_ref IS NOT NULL AND trim(u.endpoint_ref) <> ''
                 THEN 'self-hosted' END
        )::text AS source_label
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND u.model_mapping IS NOT NULL;
$$;

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

UPDATE api.roles
SET spec = ROW((spec).preset_key, array_remove((spec).permissions, 'model:invoke'::api.permission_action))::api.role_spec
WHERE 'model:invoke' = ANY ((spec).permissions)
  AND (metadata).name NOT IN ('admin', 'model-invoker');
