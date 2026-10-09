-- Model source: allow 'self-hosted' on an external endpoint.
--
-- An external endpoint can front a model this platform serves itself, and that
-- model has the same source as the internal endpoint behind it. The API-key
-- model picker tells the two rows for one model name apart by endpoint name, so
-- 'self-hosted' no longer has to stay exclusive to internal endpoints.

BEGIN;

-- 1) Stop rejecting 'self-hosted'. Type checks and pruning are unchanged.
CREATE OR REPLACE FUNCTION api.validate_external_endpoint_model_source()
RETURNS TRIGGER AS $$
DECLARE
    v_model TEXT;
    v_exposed TEXT[];
    v_pruned  JSONB;
BEGIN
    IF (NEW.spec).model_sources IS NULL THEN
        RETURN NEW;
    END IF;

    IF jsonb_typeof((NEW.spec).model_sources) <> 'object' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10241","message": "model_sources must be an object keyed by exposed model name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    FOR v_model IN SELECT jsonb_object_keys((NEW.spec).model_sources) LOOP
        -- Every value must be a JSON string. The vocabulary stays open, but the
        -- TYPE does not: ->> would happily render 123 or an object as text, and
        -- the row would store fine and then fail to unmarshal into Go's
        -- map[string]string on the next read, taking list/get and the
        -- controllers down with it.
        IF jsonb_typeof((NEW.spec).model_sources -> v_model) <> 'string' THEN
            RAISE sqlstate 'PGRST'
                USING message = format(
                    '{"code": "10242","message": "model_sources values must be strings","hint": "model %s: the source vocabulary is open, but a source has to be a string"}',
                    to_json(v_model)::text),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END LOOP;

    -- Drop sources for models this endpoint no longer serves.
    --
    -- Nothing else prunes them: removing a model from the routing leaves its
    -- source behind for good, and the suggestion list is derived from the
    -- sources in use — so one deleted model would keep a value alive for
    -- everyone, forever. Done here rather than in the form because every writer
    -- reaches this table, the CLI included.
    --
    -- Model routes supersede the legacy per-upstream mapping when present, the
    -- same precedence the gateway applies. When neither is readable there is
    -- nothing to prune against, so the map is left alone rather than emptied —
    -- a spec that arrived without upstreams has worse problems than a stale
    -- source, and silently clearing them would compound it.
    IF (NEW.spec).model_routes IS NOT NULL
       AND array_length((NEW.spec).model_routes, 1) > 0 THEN
        SELECT array_agg(r.model) INTO v_exposed
        FROM unnest((NEW.spec).model_routes) AS r
        WHERE r.model IS NOT NULL;
    ELSIF (NEW.spec).upstreams IS NOT NULL THEN
        SELECT array_agg(DISTINCT k) INTO v_exposed
        FROM unnest((NEW.spec).upstreams) AS u,
             LATERAL jsonb_object_keys(COALESCE(u.model_mapping, '{}'::jsonb)) AS k;
    END IF;

    IF v_exposed IS NOT NULL THEN
        SELECT COALESCE(jsonb_object_agg(kv.key, kv.value), '{}'::jsonb)
          INTO v_pruned
        FROM jsonb_each((NEW.spec).model_sources) AS kv
        WHERE kv.key = ANY (v_exposed);

        -- Rebuilt through jsonb_populate_record rather than assigning to
        -- NEW.spec.model_sources: that form is rejected on PostgreSQL 13, which
        -- is what the deployed control plane runs, and listing every field of
        -- the composite instead would break the next time one is added.
        NEW.spec := jsonb_populate_record(
            NEW.spec, jsonb_build_object('model_sources', v_pruned));
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 2) get_workspace_models derives 'self-hosted', not 'internal-shared', for a
-- model reached through an upstream that points at an internal endpoint.
CREATE OR REPLACE FUNCTION api.get_workspace_models(p_workspace TEXT)
RETURNS TABLE (model TEXT, source TEXT, endpoint_name TEXT, source_label TEXT)
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT DISTINCT
        (e.spec).model.name::text AS model,
        'endpoint'::text          AS source,
        (e.metadata).name::text   AS endpoint_name,
        'self-hosted'::text       AS source_label
    FROM api.endpoints e
    WHERE (e.metadata).workspace = p_workspace
      AND (e.metadata).deletion_timestamp IS NULL
      AND (e.spec).model.name IS NOT NULL
      AND trim((e.spec).model.name) <> ''
    UNION
    SELECT DISTINCT
        k::text                    AS model,
        'external_endpoint'::text  AS source,
        (ee.metadata).name::text   AS endpoint_name,
        -- An upstream with endpoint_ref is fronting an endpoint this platform
        -- runs, so the model is self-hosted without the admin having to say
        -- so. An explicit entry still wins.
        COALESCE(
            (ee.spec).model_sources ->> k,
            CASE WHEN u.endpoint_ref IS NOT NULL AND trim(u.endpoint_ref) <> ''
                 THEN 'self-hosted' END
        )::text AS source_label
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND u.model_mapping IS NOT NULL;
$$;

COMMIT;
