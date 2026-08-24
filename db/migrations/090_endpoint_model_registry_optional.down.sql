CREATE OR REPLACE FUNCTION api.validate_endpoint_model_registry()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.registry IS NULL OR trim((NEW.spec).model.registry) = ''
    THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10011","message": "spec.model.registry is required","hint": "Provide model registry"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_registry_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model_registry();
