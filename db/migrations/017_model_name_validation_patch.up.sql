-- Patch model name validation to allow uppercase, slash, and hyphen in segments
CREATE OR REPLACE FUNCTION api.validate_endpoint_model_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.name IS NULL OR trim((NEW.spec).model.name) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10007","message": "spec.model.name is required","hint": "Provide model name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF NOT (NEW.spec).model.name ~ '^[A-Za-z0-9]+(?:[._\-A-Za-z0-9]*[A-Za-z0-9])?(?:/[A-Za-z0-9]+(?:[._\-A-Za-z0-9]*[A-Za-z0-9])?)*$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10105","message": "Invalid model name format","hint": "Use alphanumeric, dots, underscores, hyphens, and optional slash-separated segments"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;