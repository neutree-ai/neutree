-- Revert the api_key permission enforcement: restore the limit RPC bodies to
-- their pre-073 form (validation gate kept, permission checks removed) and revert
-- the preset-role grants. (Admin keeps api_key:* because enum values cannot be
-- dropped; that is harmless.)

-- get_api_key_limits without the api_key:read check.
CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits JSONB;
    v_uid    UUID;
    v_limit  BIGINT;
    v_period TEXT;
    v_used   BIGINT;
BEGIN
    SELECT (spec).limits, user_id INTO v_limits, v_uid FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND OR v_uid IS DISTINCT FROM auth.uid() THEN
        RETURN NULL;
    END IF;
    v_limits := COALESCE(v_limits, '{}'::jsonb);

    v_limit := (v_limits #>> '{token_quota,limit}')::bigint;
    IF v_limit IS NOT NULL AND v_limit > 0 THEN
        v_period := COALESCE(v_limits #>> '{token_quota,period}', 'monthly');
        v_used := api.api_key_period_usage(p_id, v_period);
        v_limits := jsonb_set(
            v_limits, '{token_quota}',
            (v_limits->'token_quota')
                || jsonb_build_object('used', v_used, 'remaining', v_limit - v_used)
        );
    END IF;
    RETURN v_limits;
END;
$$;

-- set_api_key_limits without the api_key:update check.
CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    v_result api.api_keys;
BEGIN
    PERFORM api.validate_api_key_limits(p_limits);

    UPDATE api.api_keys k
    SET spec = ROW(
        COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0),
        (k.spec).expires_in,
        p_limits
    )::api.api_key_spec
    WHERE k.id = p_id AND k.user_id = auth.uid()
    RETURNING * INTO v_result;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or not owned by caller';
    END IF;
    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- Revert workspace-user permission set to the pre-073 list (without api_key).
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

-- Remove api_key:read / api_key:update from workspace-admin.
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    ARRAY(
        SELECT p FROM unnest((spec).permissions) AS p
        WHERE p NOT IN ('api_key:read','api_key:update')
    )::api.permission_action[]
)::api.role_spec
WHERE (metadata).name = 'workspace-admin';
