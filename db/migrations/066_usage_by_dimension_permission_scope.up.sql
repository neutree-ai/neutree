-- Widen api.get_usage_by_dimension from own-keys-only to
-- own-keys ∪ permission-scoped.
--
-- The function (last rewritten in 063 to source each usage row's workspace from
-- the daily-usage row instead of joining endpoints) hard-filtered to the
-- caller's own API keys (WHERE ak.user_id = auth.uid()), so usage analytics were
-- strictly per-user. It now also admits every API key in any workspace where the
-- caller holds workspace:usage-read (added in 064, granted to admin in 065). In
-- the enterprise edition has_permission is workspace-scoped, so this resolves to
-- exactly the workspaces where the caller was granted the permission; in the
-- community edition has_permission ignores the workspace argument and degrades
-- to a global check (community has no per-workspace usage control).
--
-- Only the user_api_keys CTE's WHERE clause changes relative to migration 063;
-- the daily-usage workspace sourcing introduced there is preserved verbatim.
-- api_keys resource visibility (RLS) is deliberately left untouched: this RPC
-- widens aggregate usage visibility only, not access to the key rows or their
-- plaintext secrets.
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
        WHERE (
            -- own keys: always visible to their creator
            ak.user_id = auth.uid()
            -- plus every key in any workspace where the caller holds
            -- workspace:usage-read (workspace-scoped in EE, global in CE)
            OR api.has_permission(auth.uid(), 'workspace:usage-read', (ak.metadata).workspace)
        )
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
