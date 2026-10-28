-- Observed serving names, in the order returned by the inference instance.
BEGIN;

ALTER TYPE api.endpoint_status ADD ATTRIBUTE served_models TEXT[];

-- API-key candidates use the first observed name rather than the deployment name.
CREATE OR REPLACE FUNCTION api.get_workspace_models(p_workspace TEXT)
RETURNS TABLE (model TEXT, source TEXT, endpoint_name TEXT, source_label TEXT)
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT DISTINCT
        (e.status).served_models[1] AS model,
        'endpoint'::text          AS source,
        (e.metadata).name::text   AS endpoint_name,
        'self-hosted'::text       AS source_label
    FROM api.endpoints e
    WHERE (e.metadata).workspace = p_workspace
      AND (e.metadata).deletion_timestamp IS NULL
      AND (e.status).served_models[1] IS NOT NULL
      AND trim((e.status).served_models[1]) <> ''
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
