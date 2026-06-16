-- API key UX enhancements (NEUTREE-GENERAL-9): two read RPCs for the api-key
-- pages, so the UI does not cross L2 domain boundaries and avoids N+1 on lists.
--
--  * get_workspace_models   - the client-facing model names available in a
--    workspace (endpoint spec.model.name + external-endpoint model_mapping keys),
--    tagged by source. Powers the "allowed models" dropdown.
--  * get_api_keys_usage_summary - per api_key overall quota + current-period
--    used/remaining, in one call. Powers the list resource-usage columns.
-- No table changes.

-- ----------------------
-- Available models in a workspace. SECURITY INVOKER so the caller's RLS on
-- endpoints / external_endpoints decides visibility. Client-facing names:
--   endpoint          -> spec.model.name (the served model name)
--   external_endpoint -> the keys of each upstream's model_mapping
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_workspace_models(p_workspace TEXT)
RETURNS TABLE (model TEXT, source TEXT, endpoint_name TEXT)
LANGUAGE sql STABLE SECURITY INVOKER
AS $$
    SELECT DISTINCT
        (e.spec).model.name::text AS model,
        'endpoint'::text          AS source,
        (e.metadata).name::text   AS endpoint_name
    FROM api.endpoints e
    WHERE (e.metadata).workspace = p_workspace
      AND (e.metadata).deletion_timestamp IS NULL
      AND (e.spec).model.name IS NOT NULL
      AND trim((e.spec).model.name) <> ''
    UNION
    SELECT DISTINCT
        k::text                    AS model,
        'external_endpoint'::text  AS source,
        (ee.metadata).name::text   AS endpoint_name
    FROM api.external_endpoints ee
    CROSS JOIN LATERAL unnest((ee.spec).upstreams) AS u
    CROSS JOIN LATERAL jsonb_object_keys(u.model_mapping) AS k
    WHERE (ee.metadata).workspace = p_workspace
      AND (ee.metadata).deletion_timestamp IS NULL
      AND u.model_mapping IS NOT NULL;
$$;

-- ----------------------
-- Per-API-key overall (dimension-agnostic) quota with current-period usage, for
-- one workspace, in a single call. SECURITY DEFINER (the token ledger spans keys
-- the caller may not own) guarded by workspace:read.
-- ----------------------
CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(p_workspace TEXT)
RETURNS TABLE (api_key_id UUID, period TEXT, token_limit BIGINT, used BIGINT, remaining BIGINT)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
BEGIN
    IF NOT api.has_permission(auth.uid(), 'workspace:read', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    RETURN QUERY
        SELECT
            q.api_key_id,
            q.period,
            q.limit_tokens,
            api.quota_period_usage(ARRAY[q.api_key_id], q.period, CURRENT_DATE) AS used,
            q.limit_tokens
                - api.quota_period_usage(ARRAY[q.api_key_id], q.period, CURRENT_DATE) AS remaining
        FROM api.quota_policies q
        WHERE q.level = 'api_key'
          AND q.workspace = p_workspace
          AND q.dimension_type IS NULL;
END;
$$;

NOTIFY pgrst, 'reload schema';
