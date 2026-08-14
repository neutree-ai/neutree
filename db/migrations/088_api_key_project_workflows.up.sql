BEGIN;

CREATE OR REPLACE FUNCTION api.move_api_keys_to_project(
    p_api_key_ids UUID[], p_project_id UUID
) RETURNS INTEGER LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_target api.api_key_projects; v_conflicts TEXT; v_count INTEGER;
BEGIN
    IF COALESCE(array_length(p_api_key_ids, 1), 0) = 0 THEN
        RAISE EXCEPTION 'at least one API key is required' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO v_target FROM api.api_key_projects
      WHERE id = p_project_id AND user_id = auth.uid();
    IF NOT FOUND THEN RAISE EXCEPTION 'target project not found or permission denied' USING ERRCODE = '42501'; END IF;
    IF NOT v_target.enabled THEN RAISE EXCEPTION 'target project is disabled' USING ERRCODE = '22023'; END IF;
    IF EXISTS (
        SELECT 1
        FROM unnest(p_api_key_ids) AS selected(key_id)
        LEFT JOIN api.api_keys k ON k.id = selected.key_id
        WHERE k.id IS NULL
           OR k.user_id <> auth.uid()
           OR (k.metadata).workspace <> v_target.workspace
    ) THEN
        RAISE EXCEPTION 'API key not found, permission denied, or workspace mismatch' USING ERRCODE = '42501';
    END IF;

    SELECT string_agg(conflict.name, ', ' ORDER BY conflict.name) INTO v_conflicts
    FROM (
        SELECT DISTINCT (moving.metadata).name AS name
        FROM api.api_keys moving
        JOIN api.api_keys existing
          ON existing.project_id = p_project_id
         AND (existing.metadata).name = (moving.metadata).name
         AND existing.id <> moving.id
        WHERE moving.id = ANY(p_api_key_ids)
        UNION
        SELECT DISTINCT (first_key.metadata).name
        FROM api.api_keys first_key
        JOIN api.api_keys second_key
          ON second_key.id = ANY(p_api_key_ids)
         AND (second_key.metadata).name = (first_key.metadata).name
         AND second_key.id <> first_key.id
        WHERE first_key.id = ANY(p_api_key_ids)
    ) conflict;
    IF v_conflicts IS NOT NULL THEN
        RAISE EXCEPTION 'API key name conflicts: %', v_conflicts USING ERRCODE = '23505', DETAIL = v_conflicts;
    END IF;

    INSERT INTO api.api_key_project_history (api_key_id, from_project_id, to_project_id, moved_by)
    SELECT id, project_id, p_project_id, auth.uid() FROM api.api_keys
    WHERE id = ANY(p_api_key_ids) AND project_id <> p_project_id;
    UPDATE api.api_keys SET project_id = p_project_id
    WHERE id = ANY(p_api_key_ids) AND project_id <> p_project_id;
    GET DIAGNOSTICS v_count = ROW_COUNT;
    RETURN v_count;
END;
$$;

CREATE OR REPLACE FUNCTION api.update_api_key_project(
    p_project_id UUID, p_name TEXT DEFAULT NULL, p_description TEXT DEFAULT NULL,
    p_enabled BOOLEAN DEFAULT NULL
) RETURNS api.api_key_projects LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_result api.api_key_projects; v_current api.api_key_projects;
BEGIN
    SELECT * INTO v_current FROM api.api_key_projects
    WHERE id = p_project_id AND user_id = auth.uid();
    IF NOT FOUND THEN RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501'; END IF;
    IF v_current.name = 'Default' AND (p_name IS NOT NULL OR p_enabled IS NOT NULL) THEN
        RAISE EXCEPTION 'Default Project cannot be renamed or disabled' USING ERRCODE = '22023';
    END IF;
    IF p_name IS NOT NULL AND length(trim(p_name)) = 0 THEN
        RAISE EXCEPTION 'project name is required' USING ERRCODE = '22023';
    END IF;
    UPDATE api.api_key_projects SET
        name = COALESCE(trim(p_name), name),
        description = COALESCE(p_description, description),
        enabled = COALESCE(p_enabled, enabled)
    WHERE id = p_project_id AND user_id = auth.uid()
    RETURNING * INTO v_result;
    RETURN v_result;
EXCEPTION WHEN unique_violation THEN
    RAISE EXCEPTION 'Project name already exists' USING ERRCODE = '23505';
END;
$$;

CREATE OR REPLACE FUNCTION api.delete_api_key_project(p_project_id UUID)
RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_count BIGINT; v_name TEXT;
BEGIN
    SELECT name INTO v_name FROM api.api_key_projects
    WHERE id = p_project_id AND user_id = auth.uid();
    IF NOT FOUND THEN
        RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501';
    END IF;
    IF v_name = 'Default' THEN
        RAISE EXCEPTION 'Default Project cannot be deleted' USING ERRCODE = '22023';
    END IF;
    SELECT count(*) INTO v_count FROM api.api_keys WHERE project_id = p_project_id;
    IF v_count > 0 THEN
        RAISE EXCEPTION 'Project has % API keys', v_count USING ERRCODE = '23503', DETAIL = v_count::text;
    END IF;
    DELETE FROM api.api_key_projects WHERE id = p_project_id;
END;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
