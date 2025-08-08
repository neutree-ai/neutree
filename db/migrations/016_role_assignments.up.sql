CREATE OR REPLACE FUNCTION api.validate_role_assignments()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).user_id IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10032","message": "spec.user_id is required","hint": "Provide User Information"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    IF (NEW.spec).role IS NULL OR TRIM((NEW.spec).role) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10033","message": "spec.role is required","hint": "Provide Role Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    -- Validation: workspace should not be null or empty when global is false (error code: 10034)
    IF (NEW.spec).global = false AND ((NEW.spec).workspace IS NULL OR TRIM((NEW.spec).workspace) = '') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10034","message": "spec.workspace is required","hint": "Provide Workspace Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_role_assignments_on_role_assignments
    BEFORE INSERT OR UPDATE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_role_assignments();