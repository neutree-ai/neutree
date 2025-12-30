CREATE OR REPLACE FUNCTION api.validate_endpoint_model_version()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.version IS NULL OR trim((NEW.spec).model.version) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10008","message": "spec.model.version is required","hint": "Provide model version"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_version_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model_version();
