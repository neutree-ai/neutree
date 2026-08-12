-- Fixes on top of 072:
-- 1) migrate_api_keys must also reject name collisions *among the selected
--    keys themselves* (two keys from different Projects with the same name
--    cannot both land in the target Project), reporting the names.
-- 2) validate_api_key_project must block moving a key into a disabled Project
--    via a direct table update, matching the create/migrate rules.

CREATE OR REPLACE FUNCTION api.migrate_api_keys(
    p_api_key_ids UUID[],
    p_project_id UUID
) RETURNS SETOF api.api_keys
SECURITY DEFINER AS $$
DECLARE
    v_target api.projects;
    v_key api.api_keys;
    v_conflicts TEXT;
BEGIN
    SELECT * INTO v_target FROM api.projects WHERE id = p_project_id;
    IF NOT FOUND OR v_target.status <> 'enabled' THEN
        RAISE EXCEPTION 'Migration target Project is missing or disabled';
    END IF;
    IF NOT api.has_permission(auth.uid(), 'workspace:update', v_target.workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    -- Reject the whole migration when any selected key collides with an
    -- existing key in the target Project OR with another selected key (both
    -- would share one (project_id, name) slot). Name every conflicting key so
    -- the UI can show exactly what must be renamed first.
    SELECT string_agg(name, ', ' ORDER BY name)
    INTO v_conflicts
    FROM (
        SELECT DISTINCT (k.metadata).name AS name
        FROM api.api_keys k
        JOIN api.api_keys existing
          ON existing.project_id = p_project_id
         AND (existing.metadata).name = (k.metadata).name
         AND existing.id <> k.id
        WHERE k.id = ANY(p_api_key_ids)
        UNION
        SELECT DISTINCT (k.metadata).name
        FROM api.api_keys k
        JOIN api.api_keys k2
          ON k2.id = ANY(p_api_key_ids)
         AND (k2.metadata).name = (k.metadata).name
         AND k2.id <> k.id
        WHERE k.id = ANY(p_api_key_ids)
    ) conflicts;

    IF v_conflicts IS NOT NULL THEN
        RAISE EXCEPTION 'API key name conflict in target Project: %', v_conflicts;
    END IF;

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
    IF (TG_OP = 'INSERT' OR NEW.project_id IS DISTINCT FROM OLD.project_id)
       AND v_project.status <> 'enabled' THEN
        RAISE EXCEPTION 'Project is disabled';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
