ALTER TYPE api.cluster_spec ADD ATTRIBUTE cache json;
ALTER TYPE api.cluster_status ADD ATTRIBUTE cache json;

CREATE FUNCTION api.validate_cluster_cache() RETURNS trigger AS $$
DECLARE
    config jsonb := (NEW.spec).cache::jsonb;
    owner text;
BEGIN
    IF config IS NULL OR config = 'null'::jsonb THEN RETURN NEW; END IF;
    IF jsonb_typeof(config) <> 'object'
        OR config - ARRAY['provider', 'options'] <> '{}'::jsonb
        OR jsonb_typeof(config->'provider') IS DISTINCT FROM 'string'
        OR length(btrim(config->>'provider')) = 0
        OR jsonb_typeof(config->'options') IS DISTINCT FROM 'object' THEN
        RAISE EXCEPTION 'cache requires a provider and object options' USING ERRCODE = '22023';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        owner := (OLD.status).cache::jsonb->>'provider';
        IF owner IS NOT NULL AND owner <> config->>'provider' THEN
            RAISE EXCEPTION 'remove the existing cluster cache and wait for cleanup before changing provider' USING ERRCODE = '22023';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_cluster_cache
BEFORE INSERT OR UPDATE OF spec ON api.clusters
FOR EACH ROW EXECUTE FUNCTION api.validate_cluster_cache();
