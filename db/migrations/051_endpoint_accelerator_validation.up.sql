CREATE OR REPLACE FUNCTION api.validate_endpoint_accelerator()
RETURNS TRIGGER AS $$
DECLARE
    accelerator_json json;
    key_name text;
    val_type text;
BEGIN
    accelerator_json := (NEW.spec).resources.accelerator;

    -- Skip validation if accelerator is NULL (optional field)
    IF accelerator_json IS NULL THEN
        RETURN NEW;
    END IF;

    -- Validate accelerator is a JSON object
    IF json_typeof(accelerator_json) != 'object' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10108","message": "spec.resources.accelerator must be a JSON object with string values","hint": "Accelerator should be a flat key-value map, e.g. {\"type\": \"nvidia_gpu\", \"product\": \"Tesla-V100\"}"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Validate all values are strings
    FOR key_name IN SELECT json_object_keys(accelerator_json)
    LOOP
        val_type := json_typeof(accelerator_json -> key_name);
        IF val_type != 'string' THEN
            RAISE sqlstate 'PGRST'
                USING message = format(
                    '{"code": "10108","message": "spec.resources.accelerator.%s must be a string, got %s","hint": "All accelerator values must be strings, e.g. {\"type\": \"nvidia_gpu\", \"product\": \"Tesla-V100\"}"}',
                    key_name, val_type
                ),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END LOOP;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_accelerator_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_accelerator();
