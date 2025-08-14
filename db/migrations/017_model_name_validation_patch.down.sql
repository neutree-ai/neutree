-- Restore old strict lowercase + hyphen format from migration 013
CREATE OR REPLACE FUNCTION api.validate_endpoint_model_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.name IS NULL OR trim((NEW.spec).model.name) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10007","message": "spec.model.name is required","hint": "Provide model name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF NOT (NEW.spec).model.name ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10105","message": "Invalid model name format","hint": "Use lowercase alphanumeric and hyphens"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;