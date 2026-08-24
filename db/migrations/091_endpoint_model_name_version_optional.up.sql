-- An engine that brings its own model has no model name or version to give:
-- whether either is required depends on the engine, which a trigger cannot read.
-- The requirement moves to internal/routes/proxies/endpoint_validation.go, next
-- to the model registry check migration 090 moved there for the same reason.
--
-- The name format check stays here, because it applies to whatever name is
-- given regardless of engine. Body based on 017, the latest definition.
CREATE OR REPLACE FUNCTION api.validate_endpoint_model_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.name IS NOT NULL AND trim((NEW.spec).model.name) <> ''
       AND NOT (NEW.spec).model.name ~ '^[A-Za-z0-9]+(?:[._\-A-Za-z0-9]*[A-Za-z0-9])?(?:/[A-Za-z0-9]+(?:[._\-A-Za-z0-9]*[A-Za-z0-9])?)*$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10105","message": "Invalid model name format","hint": "Use alphanumeric, dots, underscores, hyphens, and optional slash-separated segments"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Version carried no format rule, only the presence check, so the whole trigger goes.
DROP TRIGGER IF EXISTS validate_endpoint_model_version_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_model_version();
