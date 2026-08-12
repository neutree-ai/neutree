-- Restore the 072 behaviour of both functions.

CREATE OR REPLACE FUNCTION api.migrate_api_keys(
    p_api_key_ids UUID[],
    p_project_id UUID
) RETURNS SETOF api.api_keys
SECURITY DEFINER AS $$
DECLARE
    v_target api.projects;
    v_key api.api_keys;
BEGIN
    SELECT * INTO v_target FROM api.projects WHERE id = p_project_id;
    IF NOT FOUND OR v_target.status <> 'enabled' THEN
        RAISE EXCEPTION 'Migration target Project is missing or disabled';
    END IF;
    IF NOT api.has_permission(auth.uid(), 'workspace:update', v_target.workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    DECLARE
        v_conflicts TEXT;
    BEGIN
        SELECT string_agg(DISTINCT (existing.metadata).name, ', '
                          ORDER BY (existing.metadata).name)
        INTO v_conflicts
        FROM api.api_keys k
        JOIN api.api_keys existing ON existing.project_id = p_project_id
            AND (existing.metadata).name = (k.metadata).name
            AND existing.id <> k.id
        WHERE k.id = ANY(p_api_key_ids);

        IF v_conflicts IS NOT NULL THEN
            RAISE EXCEPTION 'API key name conflict in target Project: %', v_conflicts;
        END IF;
    END;

    FOR v_key IN
        SELECT k.* FROM api.api_keys k
        WHERE k.id = ANY(p_api_key_ids)
          AND (k.metadata).workspace = v_target.workspace
          AND api.has_permission(auth.uid(), 'workspace:update', (k.metadata).workspace)
    LOOP
        IF v_key.project_id <> p_project_id THEN
            INSERT INTO api.api_key_project_history(api_key_id, from_project_id, to_project_id, moved_by)
            VALUES (v_key.id, v_key.project_id, p_project_id, auth.uid());
            UPDATE api.api_keys SET project_id = p_project_id WHERE id = v_key.id;
        END IF;
        RETURN NEXT (SELECT k FROM api.api_keys k WHERE k.id = v_key.id);
    END LOOP;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION api.validate_api_key_project()
RETURNS TRIGGER AS $$
DECLARE v_project api.projects;
BEGIN
    SELECT * INTO v_project FROM api.projects WHERE id = NEW.project_id;
    IF NOT FOUND OR v_project.workspace <> (NEW.metadata).workspace THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace';
    END IF;
    IF TG_OP = 'INSERT' AND v_project.status <> 'enabled' THEN
        RAISE EXCEPTION 'Project is disabled';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
