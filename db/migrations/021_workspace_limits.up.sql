CREATE OR REPLACE FUNCTION api.enforce_global_role_assignment()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).global != TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10041","message": "Workspace-level role assignments are not supported","hint": "Use global role assignments"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    NEW.spec := ROW(
        (NEW.spec).user_id,
        NULL,
        TRUE,
        (NEW.spec).role
    )::api.role_assignment_spec;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER enforce_global_role_assignment_trigger
    BEFORE INSERT OR UPDATE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION api.enforce_global_role_assignment();

CREATE OR REPLACE FUNCTION api.validate_workspace_limit()
RETURNS TRIGGER AS $$
DECLARE
    workspace_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO workspace_count
    FROM api.workspaces
    WHERE (metadata).deletion_timestamp IS NULL;

    IF workspace_count >= 1 THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10042","message": "Workspace limit exceeded","hint": "Only 1 workspace is supported"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_workspace_limit_on_insert
    BEFORE INSERT ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_workspace_limit();