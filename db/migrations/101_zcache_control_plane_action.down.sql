-- Remove explicit control_plane intent through the authenticated API before
-- rollback so Enterprise resource signatures remain valid.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM api.clusters WHERE (spec).zcache::jsonb ? 'control_plane') THEN
  RAISE EXCEPTION 'remove zcache.control_plane through the API before rollback';
 END IF;
END $$;

CREATE OR REPLACE FUNCTION api.validate_cluster_zcache() RETURNS trigger AS $$
DECLARE
    config jsonb := (NEW.spec).zcache::jsonb;
    nodes jsonb;
    size numeric;
BEGIN
    IF config IS NULL OR config = 'null'::jsonb THEN RETURN NEW; END IF;
    IF (NEW.spec).type <> 'kubernetes' OR jsonb_typeof(config) <> 'object' THEN
        RAISE EXCEPTION 'zcache requires a Kubernetes cluster and an object configuration' USING ERRCODE = '22023';
    END IF;
    IF config - ARRAY['enabled', 'l1_size_gib', 'target_nodes'] <> '{}'::jsonb
        OR jsonb_typeof(config->'enabled') IS DISTINCT FROM 'boolean' THEN
        RAISE EXCEPTION 'invalid zcache configuration fields' USING ERRCODE = '22023';
    END IF;
    IF config ? 'l1_size_gib' OR (config->>'enabled')::boolean THEN
        IF jsonb_typeof(config->'l1_size_gib') IS DISTINCT FROM 'number' THEN
            RAISE EXCEPTION 'zcache.l1_size_gib must be an integer' USING ERRCODE = '22023';
        END IF;
        size := (config->>'l1_size_gib')::numeric;
        IF size < 0 OR size > 2147483647 OR size <> trunc(size)
            OR ((config->>'enabled')::boolean AND size = 0) THEN
            RAISE EXCEPTION 'zcache.l1_size_gib must be a positive int32 when enabled' USING ERRCODE = '22023';
        END IF;
    END IF;
    IF config ? 'target_nodes' OR (config->>'enabled')::boolean THEN
        nodes := config->'target_nodes';
        IF jsonb_typeof(nodes) IS DISTINCT FROM 'array' THEN
            RAISE EXCEPTION 'zcache.target_nodes must be an array' USING ERRCODE = '22023';
        END IF;
        IF ((config->>'enabled')::boolean AND jsonb_array_length(nodes) = 0) OR EXISTS (
            SELECT 1 FROM jsonb_array_elements(nodes) AS item
            WHERE jsonb_typeof(item) <> 'string'
                OR length(item #>> '{}') > 253
                OR (item #>> '{}') !~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$'
        ) OR (SELECT count(DISTINCT item) FROM jsonb_array_elements(nodes) AS item) <> jsonb_array_length(nodes) THEN
            RAISE EXCEPTION 'zcache.target_nodes must contain unique Kubernetes node names' USING ERRCODE = '22023';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

