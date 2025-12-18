-- ----------------------
-- Prevent deletion of default workspace
-- ----------------------
CREATE OR REPLACE FUNCTION api.prevent_default_workspace_deletion()
RETURNS TRIGGER AS $$
BEGIN
    IF (OLD.metadata).name = 'default'
      AND (OLD.metadata).deletion_timestamp IS NULL
      AND (NEW.metadata).deletion_timestamp IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10043","message": "Cannot delete default workspace","hint": "The default workspace is protected and cannot be deleted"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_default_workspace_deletion_trigger
    BEFORE UPDATE ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION api.prevent_default_workspace_deletion();