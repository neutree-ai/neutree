-- Model source label (NEU-782).
--
-- A model's "source" (来源标签) tells apart models whose cost properties differ:
-- 自建私有 / 内部共享 / 第三方公有 / 合作伙伴. It is display / grouping metadata
-- only and takes no part in any quota or access decision.
--
-- Storage: the generic metadata.labels map that every resource already carries
-- (api.metadata.labels, since 001_rbac.up.sql), under the fixed key
-- 'neutree.ai/model-source' -- the same key convention as
-- neutree.ai/cluster-version. No new column, no new type, and deliberately NO
-- CHECK constraint on the value: the enum must stay extensible, so a new source
-- value needs neither a migration nor a code change. The values stored are
-- machine-readable English slugs; the UI renders the Chinese text.
--
-- The label is stored ONLY on external endpoints. An internal endpoint's source
-- is derived as 'self-hosted': storing it would be an information-free second
-- entry an admin has to fill in.

BEGIN;

-- 1) Reject 'self-hosted' on an external endpoint.
--
-- An API key's allowed_models entries are (model, type, endpoint_name) triples
-- where type is internal/external. The same model name can be exposed by both
-- an internal endpoint and an external one -- that is exactly the case NEU-783's
-- per-model quota exists for. Once the UI replaces the internal/外部 badge with
-- this source label, the source label becomes the ONLY way a user can tell the
-- IE row apart from the EE row for that model name in the allowed_models picker.
-- While 'self-hosted' <-> IE stays one-to-one that distinction is lossless; the
-- moment an EE may also claim 'self-hosted', the two rows become
-- indistinguishable and NEU-783's per-model quota is unconfigurable in the UI.
--
-- So: 'self-hosted' is system-derived and IE-only. Every other value, preset or
-- not, is accepted here.
CREATE OR REPLACE FUNCTION api.validate_external_endpoint_model_source()
RETURNS TRIGGER AS $$
BEGIN
    IF ((NEW.metadata).labels::jsonb ->> 'neutree.ai/model-source') = 'self-hosted' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10240","message": "model source ''self-hosted'' is not allowed on an external endpoint","hint": "''self-hosted'' is derived for internal endpoints only; pick another neutree.ai/model-source value (e.g. internal-shared, third-party-public, partner)"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_model_source_on_external_endpoints
    BEFORE INSERT OR UPDATE ON api.external_endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_external_endpoint_model_source();

-- 2) get_workspace_models also returns the resolved source label, so the UI has
-- ONE uniform way to read a model's source instead of joining two tables and
-- special-casing the internal side.
--
--   internal endpoint row -> 'self-hosted' (derived constant, nothing stored)
--   external endpoint row -> the EE's metadata.labels->>'neutree.ai/model-source',
--                            NULL when unset
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
        ((ee.metadata).labels::jsonb ->> 'neutree.ai/model-source')::text AS source_label
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND u.model_mapping IS NOT NULL;
$$;

COMMIT;
