ALTER TYPE api.endpoint_spec ADD ATTRIBUTE kv_cache json;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE kv_cache json;

CREATE FUNCTION api.validate_endpoint_kv_cache() RETURNS trigger AS $$
DECLARE
    config jsonb := (NEW.spec).kv_cache::jsonb;
BEGIN
    IF config IS NOT NULL AND config <> 'null'::jsonb THEN
        IF jsonb_typeof(config) <> 'object'
            OR config - ARRAY['provider', 'options'] <> '{}'::jsonb
            OR jsonb_typeof(config->'provider') IS DISTINCT FROM 'string'
            OR config->>'provider' = ''
            OR (config ? 'options' AND jsonb_typeof(config->'options') IS DISTINCT FROM 'object') THEN
            RAISE EXCEPTION 'invalid endpoint KV cache configuration' USING ERRCODE = '22023';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_kv_cache BEFORE INSERT OR UPDATE OF spec ON api.endpoints
FOR EACH ROW EXECUTE FUNCTION api.validate_endpoint_kv_cache();

