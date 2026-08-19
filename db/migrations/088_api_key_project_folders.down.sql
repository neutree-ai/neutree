BEGIN;

DROP FUNCTION IF EXISTS api.update_api_key_configuration(UUID, UUID, JSONB, TEXT, TEXT);
DROP FUNCTION IF EXISTS api.get_api_key_project_groups(TEXT, TEXT, BOOLEAN, INTEGER, INTEGER);
DROP FUNCTION IF EXISTS api.count_api_key_project_group_api_keys(TEXT, TEXT, BOOLEAN);
DROP FUNCTION IF EXISTS api.move_api_keys_to_project(UUID[], UUID);
DROP FUNCTION IF EXISTS api.delete_api_key_project(UUID);
DROP FUNCTION IF EXISTS api.update_api_key_project(UUID, TEXT, TEXT);
DROP FUNCTION IF EXISTS api.list_api_key_projects(TEXT);
DROP FUNCTION IF EXISTS api.create_api_key_project(TEXT, TEXT, TEXT);
DROP TRIGGER IF EXISTS touch_api_key_project ON api.api_key_projects;
DROP FUNCTION IF EXISTS api.touch_api_key_project();

DROP FUNCTION api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB, UUID, TEXT);
CREATE FUNCTION api.create_api_key(
    p_workspace TEXT,
    p_name TEXT,
    p_quota INTEGER,
    p_display_name TEXT DEFAULT NULL,
    p_expires_in INTEGER DEFAULT NULL,
    p_limits JSONB DEFAULT NULL
) RETURNS api.api_keys
SECURITY DEFINER LANGUAGE plpgsql AS $$
DECLARE
    p_user_id UUID;
    v_key_id UUID;
    v_key_value TEXT;
    v_quota BIGINT;
    v_result api.api_keys;
BEGIN
    p_user_id := auth.uid();
    IF NOT EXISTS (SELECT 1 FROM api.user_profiles WHERE id = p_user_id) THEN
        RAISE EXCEPTION 'User profile not found';
    END IF;
    IF p_workspace IS NULL OR p_workspace = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10044","message": "workspace is required to create an API key","hint": "Pass p_workspace with the name of an existing workspace"}',
                  detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    IF p_display_name IS NULL THEN
        p_display_name := p_name;
    END IF;
    IF p_limits IS NULL AND p_quota IS NOT NULL AND p_quota > 0 THEN
        p_limits := jsonb_build_object(
            'token_quota', jsonb_build_object('limit', p_quota, 'period', 'monthly')
        );
    END IF;
    PERFORM api.validate_api_key_limits(p_limits);
    v_quota := COALESCE((p_limits #>> '{token_quota,limit}')::bigint, 0);
    v_key_id := gen_random_uuid();
    v_key_value := api.generate_api_key(p_user_id, v_key_id, p_expires_in);
    INSERT INTO api.api_keys (
        id, api_version, kind, metadata, spec, status, user_id
    ) VALUES (
        v_key_id,
        'v1',
        'ApiKey',
        ROW(
            p_name, p_display_name, p_workspace, NULL, now(), now(),
            '{}'::json, '{}'::json
        )::api.metadata,
        ROW(v_quota, p_expires_in, p_limits)::api.api_key_spec,
        ROW('Pending', now(), NULL, v_key_value, 0, now(), NULL)::api.api_key_status,
        p_user_id
    )
    RETURNING * INTO v_result;
    RETURN v_result;
END;
$$;

DROP INDEX IF EXISTS api.api_keys_project_id_idx;
ALTER TABLE api.api_keys DROP COLUMN description, DROP COLUMN project_id;
DROP TABLE api.api_key_projects;

NOTIFY pgrst, 'reload schema';
COMMIT;
