BEGIN;

-- A workspace user may inspect quota usage for their own API keys. Users with
-- workspace:usage-read retain the workspace-wide view used by administrators.
CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(p_workspace TEXT)
RETURNS TABLE (
    api_key_id UUID,
    period TEXT,
    token_limit BIGINT,
    used BIGINT,
    remaining BIGINT
)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
DECLARE
    v_can_read_workspace BOOLEAN;
BEGIN
    v_can_read_workspace := api.has_permission(
        auth.uid(), 'workspace:usage-read', p_workspace
    );

    RETURN QUERY
        SELECT
            k.id,
            lim.period,
            lim.token_limit,
            COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used,
            lim.token_limit - COALESCE(SUM((d.spec).total_usage), 0)::bigint AS remaining
        FROM api.api_keys k
        CROSS JOIN LATERAL (
            SELECT
                COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly') AS period,
                ((k.spec).limits #>> '{token_quota,limit}')::bigint AS token_limit,
                CASE COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly')
                    WHEN 'daily' THEN CURRENT_DATE
                    WHEN 'weekly' THEN date_trunc('week', CURRENT_DATE)::date
                    WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                    WHEN 'yearly' THEN date_trunc('year', CURRENT_DATE)::date
                    ELSE date_trunc('month', CURRENT_DATE)::date
                END AS period_start
        ) lim
        LEFT JOIN api.api_daily_usage d
            ON (d.spec).api_key_id = k.id
           AND (d.spec).usage_date >= lim.period_start
           AND (d.spec).usage_date <= CURRENT_DATE
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND (k.user_id = auth.uid() OR v_can_read_workspace)
          AND ((k.spec).limits #>> '{token_quota,limit}') IS NOT NULL
          AND ((k.spec).limits #>> '{token_quota,limit}')::bigint > 0
        GROUP BY k.id, lim.period, lim.token_limit;
END;
$$;

-- The usage RPC is the authority for which keys a caller may aggregate. Return
-- user-facing identity alongside the immutable technical name so every usage
-- view can render the same label without widening api_keys RLS.
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
    api_key_display_name TEXT,
    api_key_description TEXT,
    endpoint_type TEXT,
    endpoint_name TEXT,
    model_name TEXT,
    workspace TEXT,
    usage BIGINT,
    prompt_tokens BIGINT,
    completion_tokens BIGINT
)
SECURITY DEFINER
LANGUAGE plpgsql
AS $$
BEGIN
    RETURN QUERY
    WITH user_api_keys AS (
        SELECT
            ak.id,
            (ak.metadata).name AS key_name,
            (ak.metadata).display_name AS key_display_name,
            (ak.spec).description AS key_description,
            (ak.metadata).workspace AS key_workspace
        FROM api.api_keys ak
        WHERE (
            ak.user_id = auth.uid()
            OR api.has_permission(
                auth.uid(), 'workspace:usage-read', (ak.metadata).workspace
            )
        )
        AND (p_api_key_id IS NULL OR ak.id = p_api_key_id)
    ), old_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            NULL::text AS endpoint_type,
            kv.key AS endpoint_name,
            NULL::text AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value)::bigint AS dimension_usage,
            NULL::bigint AS p_tokens,
            NULL::bigint AS c_tokens
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NULL
    ), new_dimension_data AS (
        SELECT
            (u.spec).usage_date,
            (u.spec).api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            split_part(kv.key, '|', 1) AS endpoint_type,
            split_part(kv.key, '|', 2) AS endpoint_name,
            NULLIF(split_part(kv.key, '|', 3), '') AS model_name,
            COALESCE((u.metadata).workspace, k.key_workspace, 'unknown') AS workspace,
            (kv.value->>'total')::bigint AS dimension_usage,
            (kv.value->>'prompt')::bigint AS p_tokens,
            (kv.value->>'completion')::bigint AS c_tokens
        FROM api.api_daily_usage u
        JOIN user_api_keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).detailed_dimensional_usage) kv
        WHERE (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NOT NULL
    ), dimension_data AS (
        SELECT * FROM old_dimension_data
        UNION ALL
        SELECT * FROM new_dimension_data
    )
    SELECT
        d.usage_date,
        d.api_key_id,
        d.key_name,
        d.key_display_name,
        d.key_description,
        d.endpoint_type,
        d.endpoint_name,
        d.model_name,
        d.workspace,
        d.dimension_usage,
        d.p_tokens,
        d.c_tokens
    FROM dimension_data d
    WHERE (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name)
      AND (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY d.usage_date DESC, d.api_key_id, d.endpoint_name;
END;
$$;

-- Wrap the existing grouping implementation so upgraded installations also
-- receive redacted API key objects. Fresh installations are already redacted
-- by migration 091; applying the operation twice is harmless.
ALTER FUNCTION api.get_api_key_project_groups(TEXT, TEXT, BOOLEAN, INTEGER, INTEGER)
    RENAME TO get_api_key_project_groups_unredacted;

CREATE FUNCTION api.get_api_key_project_groups(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL,
    p_page INTEGER DEFAULT 1,
    p_page_size INTEGER DEFAULT 20
) RETURNS TABLE (
    project JSONB,
    api_keys JSONB,
    api_key_count BIGINT,
    current_usage BIGINT,
    total_projects BIGINT
) LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT
        grouped.project,
        COALESCE(
            (
                SELECT jsonb_agg(api_key #- '{status,sk_value}')
                FROM jsonb_array_elements(grouped.api_keys) AS api_key
            ),
            '[]'::jsonb
        ),
        grouped.api_key_count,
        grouped.current_usage,
        grouped.total_projects
    FROM api.get_api_key_project_groups_unredacted(
        p_workspace,
        p_search,
        p_api_key_disabled,
        p_page,
        p_page_size
    ) AS grouped;
$$;

COMMIT;
