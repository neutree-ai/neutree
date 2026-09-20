-- Model source (NEU-782).
--
-- A model's "source" (来源标签) tells apart models whose cost properties differ:
-- 自建私有 / 内部共享 / 第三方公有 / 合作伙伴. It is display / grouping metadata
-- only and takes no part in any quota or access decision.
--
-- GRANULARITY: per MODEL, not per endpoint. One external endpoint routinely
-- fronts models of different origin -- the shipped demo endpoint has one
-- upstream pointing at an internal endpoint and another at a public API, so its
-- two models genuinely have different sources. 095_external_endpoint_model_routes
-- makes this sharper still: a single model may have several targets across
-- upstreams (primary on a public API, failover onto a self-hosted one), so even
-- per-upstream would not resolve to one source per model. The source is an
-- assertion an admin makes ABOUT A MODEL, not something derivable from routing.
--
-- Storage: a map on the spec, keyed by the CLIENT-FACING model name -- the same
-- names get_workspace_models enumerates, and the names allowed_models entries
-- and quotas are written in. Deliberately no CHECK constraint on the values:
-- the enum must stay extensible, so a new source needs neither a migration nor
-- a code change. Internal endpoints store nothing at all; their source is
-- derived (see below).

BEGIN;

ALTER TYPE api.external_endpoint_spec ADD ATTRIBUTE model_sources JSONB;

-- 1) Reject 'self-hosted' as a model source on an external endpoint.
--
-- An API key's allowed_models entries are (model, type, endpoint_name) triples
-- where type is internal/external. The same model name can be exposed by both
-- an internal endpoint and an external one -- that is exactly the case NEU-783's
-- per-model quota exists for. Once the UI replaces the internal/外部 badge with
-- this source, the source becomes the ONLY way a user can tell the IE row apart
-- from the EE row for that model name in the allowed_models picker. While
-- 'self-hosted' <-> IE stays one-to-one that distinction is lossless; the moment
-- an EE may also claim 'self-hosted', the two rows become indistinguishable and
-- NEU-783's per-model quota is unconfigurable in the UI.
--
-- So: 'self-hosted' is system-derived and IE-only. Every other value, preset or
-- not, is accepted here.
CREATE OR REPLACE FUNCTION api.validate_external_endpoint_model_source()
RETURNS TRIGGER AS $$
DECLARE
    v_model TEXT;
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
        IF ((NEW.spec).model_sources ->> v_model) = 'self-hosted' THEN
            RAISE sqlstate 'PGRST'
                USING message = format(
                    '{"code": "10240","message": "model source ''self-hosted'' is not allowed on an external endpoint","hint": "model %s: ''self-hosted'' is derived for internal endpoints only; pick another source (e.g. internal-shared, third-party-public, partner)"}',
                    to_json(v_model)::text),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END LOOP;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_model_source_on_external_endpoints
    BEFORE INSERT OR UPDATE ON api.external_endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_external_endpoint_model_source();

-- 2) get_workspace_models also returns the resolved source, so the UI has ONE
-- uniform way to read it instead of joining two tables and special-casing the
-- internal side.
--
--   internal endpoint row -> 'self-hosted' (derived constant, nothing stored)
--   external endpoint row -> (spec).model_sources ->> <the exposed model name>,
--                            NULL when unset
--
-- The function already emits one row per (model, endpoint), which is exactly the
-- granularity the source is stored at, so the lookup is per row.
--
-- Existing columns (model, source, endpoint_name) keep their names and meaning;
-- `source` stays the 'endpoint' | 'external_endpoint' row origin and is NOT the
-- source label. Changing RETURNS TABLE needs a DROP first.
DROP FUNCTION IF EXISTS api.get_workspace_models(TEXT);

CREATE FUNCTION api.get_workspace_models(p_workspace TEXT)
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
        ((ee.spec).model_sources ->> k)::text AS source_label
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND u.model_mapping IS NOT NULL;
$$;

COMMIT;
