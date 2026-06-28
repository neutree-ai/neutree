-- Grant the new api_key permissions to preset roles and enforce them on the
-- api-key limit read/write RPCs, on top of the existing owner check.

-- 1) Preset role grants (mirrors the external_endpoint rollout).
-- Admin: regrant all enum permissions (picks up the new api_key:* values).
SELECT api.update_admin_permissions();

-- Workspace admin: api_key limit read + write.
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    (spec).permissions || ARRAY[
        'api_key:read',
        'api_key:update'
    ]::api.permission_action[]
)::api.role_spec
WHERE (metadata).name = 'workspace-admin';

-- Workspace user: api_key limit read + write of their own keys (self-service).
-- Kept in update_workspace_user_permissions so a future regrant preserves it.
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
        'api_key:read',
        'api_key:update'
    ]::api.permission_action[];

    UPDATE api.roles
    SET spec = ROW((spec).preset_key, workspace_user_permissions)::api.role_spec
    WHERE (metadata).name = 'workspace-user';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_workspace_user_permissions();

-- 2) get_api_key_limits: require the caller to own the key AND hold api_key:read
-- in the key's workspace (soft-deny -> NULL, consistent with the prior non-owner
-- behavior). Body otherwise unchanged.
CREATE OR REPLACE FUNCTION api.get_api_key_limits(p_id UUID)
RETURNS JSONB
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_limits JSONB;
    v_uid    UUID;
    v_ws     TEXT;
    v_limit  BIGINT;
    v_period TEXT;
    v_used   BIGINT;
BEGIN
    SELECT (spec).limits, user_id, (metadata).workspace INTO v_limits, v_uid, v_ws
    FROM api.api_keys WHERE id = p_id;
    -- NULL-safe owner check: a NULL auth.uid() (anon) must not slip past `<>`.
    IF NOT FOUND OR v_uid IS DISTINCT FROM auth.uid() THEN
        RETURN NULL;
    END IF;
    -- Reading limits requires api_key:read in the key's workspace.
    IF NOT api.has_permission(auth.uid(), 'api_key:read', v_ws) THEN
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

-- 3) set_api_key_limits: require the caller to own the key AND hold
-- api_key:update in the key's workspace. Validation gate unchanged. This also
-- governs disabling a key, which the UI performs via set_api_key_limits.
CREATE OR REPLACE FUNCTION api.set_api_key_limits(p_id UUID, p_limits JSONB)
RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    v_result api.api_keys;
    v_owner  UUID;
    v_ws     TEXT;
BEGIN
    SELECT user_id, (metadata).workspace INTO v_owner, v_ws
    FROM api.api_keys WHERE id = p_id;
    IF NOT FOUND OR v_owner IS DISTINCT FROM auth.uid() THEN
        RAISE EXCEPTION 'API key not found or not owned by caller';
    END IF;

    IF NOT api.has_permission(auth.uid(), 'api_key:update', v_ws) THEN
        RAISE EXCEPTION 'permission denied: api_key:update required' USING ERRCODE = '42501';
    END IF;

    PERFORM api.validate_api_key_limits(p_limits);

    UPDATE api.api_keys k
    SET spec = ROW(
        COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0),
        (k.spec).expires_in,
        p_limits
    )::api.api_key_spec
    WHERE k.id = p_id AND k.user_id = auth.uid()
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;
