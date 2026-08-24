BEGIN;

-- Undo the redaction wrapper: restore the single grouping function.
DROP FUNCTION IF EXISTS api.get_api_key_project_groups(TEXT, TEXT, BOOLEAN, INTEGER, INTEGER);
ALTER FUNCTION api.get_api_key_project_groups_unredacted(TEXT, TEXT, BOOLEAN, INTEGER, INTEGER)
    RENAME TO get_api_key_project_groups;

-- Restore api.get_usage_by_dimension without the display-identity columns,
-- exactly as migration 066 left it. The up migration DROP/CREATEd it to widen
-- the result set, so dropping the wrapper alone would strand the new shape.
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

-- Restore api.get_api_keys_usage_summary to its migration 070 form, which
-- gated the whole result on workspace:usage-read instead of also admitting a
-- caller's own keys.
CREATE OR REPLACE FUNCTION api.get_api_keys_usage_summary(p_workspace TEXT)
RETURNS TABLE (api_key_id UUID, period TEXT, token_limit BIGINT, used BIGINT, remaining BIGINT)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
BEGIN
    IF NOT api.has_permission(auth.uid(), 'workspace:usage-read', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;
    RETURN QUERY
        -- Single pass: derive each key's period / limit / period-start once, then
        -- LEFT JOIN + GROUP BY aggregates api_daily_usage in one scan rather than
        -- calling the ledger-summing helper once per key.
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
                    WHEN 'daily'   THEN CURRENT_DATE
                    WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
                    WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                    WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
                    ELSE date_trunc('month', CURRENT_DATE)::date
                END AS period_start
        ) lim
        LEFT JOIN api.api_daily_usage d
            ON (d.spec).api_key_id = k.id
           AND (d.spec).usage_date >= lim.period_start
           AND (d.spec).usage_date <= CURRENT_DATE
        WHERE (k.metadata).workspace = p_workspace
          AND (k.metadata).deletion_timestamp IS NULL
          AND ((k.spec).limits #>> '{token_quota,limit}') IS NOT NULL
          AND ((k.spec).limits #>> '{token_quota,limit}')::bigint > 0
        GROUP BY k.id, lim.period, lim.token_limit;
END;
$$;

NOTIFY pgrst, 'reload schema';

COMMIT;
