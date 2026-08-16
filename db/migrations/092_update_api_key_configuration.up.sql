BEGIN;

-- Save the editable Project and limits panels as one transaction so the UI
-- cannot report a partially applied API key edit.
CREATE OR REPLACE FUNCTION api.update_api_key_configuration(
    p_api_key_id UUID, p_project_id UUID, p_limits JSONB
) RETURNS api.api_keys LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE
    v_key api.api_keys;
    v_target api.api_key_projects;
BEGIN
    SELECT * INTO v_key
    FROM api.api_keys
    WHERE id = p_api_key_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'API key not found or permission denied' USING ERRCODE = '42501';
    END IF;

    SELECT * INTO v_target
    FROM api.api_key_projects
    WHERE id = p_project_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'target project not found or permission denied' USING ERRCODE = '42501';
    END IF;
    IF NOT v_target.enabled THEN
        RAISE EXCEPTION 'target project is disabled' USING ERRCODE = '22023';
    END IF;
    IF v_target.workspace <> (v_key.metadata).workspace THEN
        RAISE EXCEPTION 'API key and Project must use the same workspace' USING ERRCODE = '22023';
    END IF;

    IF v_key.project_id <> p_project_id THEN
        INSERT INTO api.api_key_project_history (
            api_key_id, from_project_id, to_project_id, moved_by,
            from_project_name, to_project_name, workspace
        )
        SELECT p_api_key_id, v_key.project_id, p_project_id, auth.uid(),
               source.name, v_target.name, v_target.workspace
        FROM api.api_key_projects source
        WHERE source.id = v_key.project_id;

        UPDATE api.api_keys
        SET project_id = p_project_id
        WHERE id = p_api_key_id;
    END IF;

    PERFORM api.set_api_key_limits(p_api_key_id, COALESCE(p_limits, '{}'::jsonb));

    SELECT * INTO v_key FROM api.api_keys WHERE id = p_api_key_id;
    RETURN v_key;
END;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
