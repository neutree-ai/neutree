ALTER TYPE api.endpoint_spec ADD ATTRIBUTE zcache json;
ALTER TYPE api.endpoint_status ADD ATTRIBUTE zcache json;

-- Lock the cluster row for cache admission. Cluster changes already lock that
-- same row, so two API replicas cannot admit a dependency while disabling it.
CREATE FUNCTION api.guard_endpoint_zcache() RETURNS trigger AS $$
DECLARE
    config jsonb := (NEW.spec).zcache::jsonb;
    old_status jsonb;
    incoming_status jsonb := (NEW.status).zcache::jsonb;
    cache_config jsonb;
    generation bigint;
BEGIN
    IF TG_OP = 'UPDATE' THEN old_status := (OLD.status).zcache::jsonb; END IF;
    IF config IS NOT NULL AND config <> 'null'::jsonb THEN
        IF jsonb_typeof(config) <> 'object'
            OR config - ARRAY['enabled', 'timeout_seconds'] <> '{}'::jsonb
            OR jsonb_typeof(config->'enabled') IS DISTINCT FROM 'boolean' THEN
            RAISE EXCEPTION 'invalid endpoint zcache configuration' USING ERRCODE = '22023';
        END IF;
        IF config ? 'timeout_seconds' THEN
            IF jsonb_typeof(config->'timeout_seconds') IS DISTINCT FROM 'number' THEN
                RAISE EXCEPTION 'cache timeout must be a number' USING ERRCODE = '22023';
            END IF;
            IF (config->>'timeout_seconds')::numeric NOT BETWEEN 0.1 AND 60 THEN
                RAISE EXCEPTION 'cache timeout must be between 0.1 and 60 seconds' USING ERRCODE = '22023';
            END IF;
        END IF;
    END IF;

    IF TG_OP = 'INSERT' OR to_jsonb(NEW.spec) IS DISTINCT FROM to_jsonb(OLD.spec) THEN
        IF COALESCE((config->>'enabled')::boolean, false) THEN
            SELECT (spec).zcache::jsonb INTO cache_config FROM api.clusters
            WHERE (metadata).name = (NEW.spec).cluster AND (metadata).workspace = (NEW.metadata).workspace
            AND (metadata).deletion_timestamp IS NULL AND (spec).type = 'kubernetes'
            FOR UPDATE;
            IF NOT COALESCE((cache_config->>'enabled')::boolean, false) THEN
                RAISE EXCEPTION 'enable cluster cache before using it in an inference instance' USING ERRCODE = '22023';
            END IF;
        END IF;
        IF COALESCE((config->>'enabled')::boolean, false) OR old_status IS NOT NULL THEN
            generation := COALESCE((old_status->>'generation')::bigint, 0) + 1;
            NEW.status.zcache := jsonb_build_object('generation', generation, 'in_use', true)::json;
        END IF;
    ELSIF old_status IS NOT NULL THEN
        -- Status-only writes on errors preserve the reservation. Observations
        -- from a previous spec must never release a newer deployment's claim.
        IF incoming_status IS NULL OR incoming_status->>'generation' IS DISTINCT FROM old_status->>'generation' THEN
            NEW.status.zcache := old_status::json;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = api, public;

CREATE TRIGGER guard_endpoint_zcache BEFORE INSERT OR UPDATE ON api.endpoints
FOR EACH ROW EXECUTE FUNCTION api.guard_endpoint_zcache();

CREATE FUNCTION api.guard_cluster_zcache_dependencies() RETURNS trigger AS $$
DECLARE
    old_config jsonb := (OLD.spec).zcache::jsonb;
    new_config jsonb := (NEW.spec).zcache::jsonb;
BEGIN
    IF COALESCE((old_config->>'enabled')::boolean, false) AND (
        NOT COALESCE((new_config->>'enabled')::boolean, false)
        OR NOT COALESCE(new_config->'target_nodes', '[]'::jsonb) @> COALESCE(old_config->'target_nodes', '[]'::jsonb)
    ) AND EXISTS (
        SELECT 1 FROM api.endpoints e WHERE (e.metadata).workspace = (OLD.metadata).workspace
        AND (e.spec).cluster = (OLD.metadata).name
        AND (COALESCE(((e.spec).zcache::jsonb->>'enabled')::boolean, false)
             OR COALESCE(((e.status).zcache::jsonb->>'in_use')::boolean, false))
    ) THEN
        RAISE EXCEPTION 'inference instances still use ZCache; disable their cache and wait for deployment to finish before removing cache nodes'
            USING ERRCODE = '22023';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = api, public;

CREATE TRIGGER guard_cluster_zcache_dependencies BEFORE UPDATE OF spec ON api.clusters
FOR EACH ROW EXECUTE FUNCTION api.guard_cluster_zcache_dependencies();

