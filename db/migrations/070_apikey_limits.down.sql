-- Revert API key limits (spec.limits).

DROP FUNCTION IF EXISTS api.get_api_keys_usage_summary(TEXT);
DROP FUNCTION IF EXISTS api.get_workspace_models(TEXT);
DROP FUNCTION IF EXISTS api.get_api_key_limits(UUID);
DROP FUNCTION IF EXISTS api.get_api_key_remaining(UUID);
DROP FUNCTION IF EXISTS api.api_key_period_usage(UUID, TEXT);
DROP FUNCTION IF EXISTS api.set_api_key_limits(UUID, JSONB);

-- Restore create_api_key to the 019 signature/body (without p_limits).
DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB);
CREATE OR REPLACE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL
) RETURNS api.api_keys
SECURITY DEFINER
AS $$
DECLARE
    p_user_id UUID;
    v_key_id UUID;
    v_key_value TEXT;
    v_result api.api_keys;
BEGIN
    p_user_id = auth.uid();

    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;

    IF p_display_name IS NULL THEN
        p_display_name := p_name;
    END IF;

    v_key_id := gen_random_uuid();
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);

    INSERT INTO api.api_keys (
        id, api_version, kind, metadata, spec, status, user_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(p_name, p_display_name, p_workspace, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
        ROW(p_quota, p_expires_in)::api.api_key_spec,
        ROW('Pending', CURRENT_TIMESTAMP, NULL, v_key_value, 0, CURRENT_TIMESTAMP, NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;

    RETURN v_result;
END;
$$ LANGUAGE plpgsql;

-- Drop the added attribute (CASCADE drops dependents on the column type if any).
ALTER TYPE api.api_key_spec DROP ATTRIBUTE IF EXISTS limits CASCADE;

NOTIFY pgrst, 'reload schema';
