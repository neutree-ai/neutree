-- Fix cross-workspace bleed in get_usage_by_dimension (NEU-463).
--
-- Previously the workspace of each usage row was derived by joining the
-- dimension's endpoint_name against api.endpoints / api.external_endpoints and
-- reading that endpoint's workspace. endpoint names are only unique *within* a
-- workspace (unique index is workspace+name), so a name-only join:
--   1. mixed usage from same-named endpoints across different workspaces, and
--   2. dropped historical usage once an endpoint was deleted (the join — and
--      therefore the workspace filter — produced no row).
--
-- api_daily_usage already records the owning API key's workspace in
-- metadata.workspace at aggregation time, so the workspace is sourced from the
-- daily-usage row (falling back to the API key's current workspace), and the
-- endpoint joins are dropped entirely. This makes both the returned workspace
-- and the p_workspace filter stable across same-named endpoints and endpoint
-- deletion. The function signature and result columns are unchanged.
DROP FUNCTION IF EXISTS api.get_usage_by_dimension;
CREATE FUNCTION api.get_usage_by_dimension(
    p_start_date DATE,
    p_end_date DATE,
    p_api_key_id UUID DEFAULT NULL,
    p_endpoint_name TEXT DEFAULT NULL,
    p_workspace TEXT DEFAULT NULL
)
RETURNS TABLE (
    date DATE,
    api_key_id UUID,
    api_key_name TEXT,
    endpoint_type TEXT,
    endpoint_name TEXT,
    model_name TEXT,
    workspace TEXT,
    usage BIGINT,
    prompt_tokens BIGINT,
    completion_tokens BIGINT
)
SECURITY DEFINER
AS $$
BEGIN
    RETURN QUERY
    WITH user_api_keys AS (
        SELECT
            ak.id,
            (ak.metadata).name AS key_name,
            (ak.metadata).workspace AS key_workspace
        FROM api.api_keys ak
        WHERE ak.user_id = auth.uid()
        AND (p_api_key_id IS NULL OR ak.id = p_api_key_id)
    ),
    -- Old data: records without detailed_dimensional_usage
    old_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            NULL::text AS endpoint_type,
            kv.key AS endpoint_name,
            NULL::text AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value)::bigint AS dimension_usage,
            NULL::bigint AS p_tokens,
            NULL::bigint AS c_tokens
        FROM
            api.api_daily_usage u
            JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
            jsonb_each((u.spec).dimensional_usage) kv
        WHERE
            (u.spec).usage_date BETWEEN p_start_date AND p_end_date
            AND (u.spec).detailed_dimensional_usage IS NULL
    ),
    -- New data: records with detailed_dimensional_usage
    new_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            split_part(kv.key, '|', 1) AS endpoint_type,
            split_part(kv.key, '|', 2) AS endpoint_name,
            NULLIF(split_part(kv.key, '|', 3), '') AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value->>'total')::bigint AS dimension_usage,
            (kv.value->>'prompt')::bigint AS p_tokens,
            (kv.value->>'completion')::bigint AS c_tokens
        FROM
            api.api_daily_usage u
            JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
            jsonb_each((u.spec).detailed_dimensional_usage) kv
        WHERE
            (u.spec).usage_date BETWEEN p_start_date AND p_end_date
            AND (u.spec).detailed_dimensional_usage IS NOT NULL
    ),
    dimension_data AS (
        SELECT * FROM old_dimension_data
        UNION ALL
        SELECT * FROM new_dimension_data
    )
    SELECT
        d.usage_date,
        d.api_key_id,
        d.key_name,
        d.endpoint_type,
        d.endpoint_name,
        d.model_name,
        d.workspace,
        d.dimension_usage,
        d.p_tokens,
        d.c_tokens
    FROM
        dimension_data d
    WHERE
        (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name) AND
        (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY
        d.usage_date DESC,
        d.api_key_id,
        d.endpoint_name;
END;
$$ LANGUAGE plpgsql;
