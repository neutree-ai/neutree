
CREATE OR REPLACE FUNCTION api.validate_endpoint_model()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.name IS NULL OR (NEW.spec).model.name = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10301","message": "spec.model is required","hint": "Provide model name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF NOT (NEW.spec).model.name ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10302","message": "Invalid model format","hint": "Use lowercase alphanumeric and hyphens"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model();