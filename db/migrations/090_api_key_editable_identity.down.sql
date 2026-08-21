BEGIN;

DROP FUNCTION api.update_api_key_configuration(UUID, UUID, JSONB, TEXT, TEXT);
CREATE FUNCTION api.update_api_key_configuration(
    p_api_key_id UUID, p_project_id UUID, p_limits JSONB
) RETURNS api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_key api.api_keys; v_workspace TEXT;
BEGIN
    SELECT * INTO v_key
    FROM api.api_keys
    WHERE id = p_api_key_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
    END IF;

    v_workspace := (v_key.metadata).workspace;
    IF p_project_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM api.api_key_projects
        WHERE id = p_project_id
          AND user_id = auth.uid()
          AND (metadata).workspace = v_workspace
    ) THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace' USING ERRCODE = '22023';
    END IF;

    UPDATE api.api_keys
    SET spec = ROW(
        (spec).quota, (spec).expires_in, (spec).limits,
        p_project_id, (spec).description
    )::api.api_key_spec
    WHERE id = p_api_key_id;
    PERFORM api.set_api_key_limits(p_api_key_id, COALESCE(p_limits, '{}'::jsonb));
    SELECT * INTO v_key FROM api.api_keys WHERE id = p_api_key_id;
    RETURN v_key;
END;
$$;

NOTIFY pgrst, 'reload schema';

COMMIT;
