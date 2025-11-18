------------------------------------
-- Rollback fix duplicate error code 10011
-- Revert Model Registry URL validation back to error code 10011
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_model_registry_url()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).url IS NULL OR trim((NEW.spec).url) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10011","message": "spec.url is required","hint": "Provide Model Registry URL"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
