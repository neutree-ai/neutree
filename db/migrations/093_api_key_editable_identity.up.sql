BEGIN;

DROP FUNCTION api.update_api_key_configuration(UUID, UUID, JSONB);
CREATE FUNCTION api.update_api_key_configuration(
    p_api_key_id UUID,
    p_project_id UUID,
    p_limits JSONB,
    p_display_name TEXT,
    p_description TEXT
) RETURNS api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_key api.api_keys; v_workspace TEXT;
BEGIN
    SELECT * INTO v_key
    FROM api.api_keys
    WHERE id = p_api_key_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
    END IF;

    IF p_display_name IS NULL OR length(trim(p_display_name)) = 0 THEN
        RAISE EXCEPTION 'API key display name is required' USING ERRCODE = '22023';
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
    SET metadata = ROW(
            (metadata).name,
            trim(p_display_name),
            (metadata).workspace,
            (metadata).deletion_timestamp,
            (metadata).creation_timestamp,
            now(),
            (metadata).labels,
            (metadata).annotations
        )::api.metadata,
        spec = ROW(
            (spec).quota, (spec).expires_in, (spec).limits,
            p_project_id, COALESCE(p_description, '')
        )::api.api_key_spec
    WHERE id = p_api_key_id;
    -- NULL means "leave the limits alone". Coalescing to '{}' instead made an
    -- omitted argument wipe every limit, reset the quota to 0 and drop the
    -- disabled flag.
    IF p_limits IS NOT NULL THEN
        PERFORM api.set_api_key_limits(p_api_key_id, p_limits);
    END IF;
    SELECT * INTO v_key FROM api.api_keys WHERE id = p_api_key_id;
    RETURN v_key;
END;
$$;

NOTIFY pgrst, 'reload schema';

COMMIT;
