BEGIN;

CREATE OR REPLACE FUNCTION api.validate_project_workspace()
RETURNS TRIGGER AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM api.workspaces w
        WHERE (w.metadata).name = NEW.workspace
          AND (w.metadata).deletion_timestamp IS NULL
    ) THEN
        RAISE EXCEPTION 'workspace not found'
            USING ERRCODE = 'P0001';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER projects_validate_workspace
    BEFORE INSERT OR UPDATE OF workspace ON api.projects
    FOR EACH ROW EXECUTE FUNCTION api.validate_project_workspace();

CREATE OR REPLACE FUNCTION api.reject_project_with_api_keys()
RETURNS TRIGGER AS $$
DECLARE
    v_count BIGINT;
BEGIN
    SELECT count(*) INTO v_count
    FROM api.api_keys
    WHERE project_id = OLD.id;

    IF v_count > 0 THEN
        RAISE EXCEPTION 'project has % associated API keys', v_count
            USING ERRCODE = '23503';
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER projects_reject_nonempty_delete
    BEFORE DELETE ON api.projects
    FOR EACH ROW EXECUTE FUNCTION api.reject_project_with_api_keys();

NOTIFY pgrst, 'reload schema';
COMMIT;
