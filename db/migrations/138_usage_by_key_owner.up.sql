-- Usage by API key of one owner: get_usage_by_dimension gets p_user_id and
-- p_service_account_id, which narrow it to that owner's keys, and p_direct,
-- which reads p_org_unit without its subtree, as get_usage_by_group does.
-- Both only narrow: what the caller may read is unchanged.
DROP FUNCTION IF EXISTS api.get_usage_by_dimension(DATE, DATE, UUID, TEXT, TEXT, TEXT, TEXT);

CREATE FUNCTION api.get_usage_by_dimension(
    p_start_date DATE,
    p_end_date DATE,
    p_api_key_id UUID DEFAULT NULL,
    p_endpoint_name TEXT DEFAULT NULL,
    p_workspace TEXT DEFAULT NULL,
    p_team TEXT DEFAULT NULL,
    p_org_unit TEXT DEFAULT NULL,
    p_user_id UUID DEFAULT NULL,
    p_service_account_id UUID DEFAULT NULL,
    p_direct BOOLEAN DEFAULT FALSE
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
    completion_tokens BIGINT,
    cache_read_tokens BIGINT,
    cache_creation_tokens BIGINT,
    reasoning_tokens BIGINT,
    cost_usd NUMERIC
)
SECURITY DEFINER
LANGUAGE plpgsql
AS $$
DECLARE
    v_uid UUID := auth.uid();
    v_scoped BOOLEAN := p_team IS NOT NULL OR p_org_unit IS NOT NULL;
    -- Teams and OrgUnits whose usage the caller reads.
    v_teams INTEGER[] := api.readable_usage_team_ids(v_uid);
    v_units INTEGER[] := api.readable_usage_org_unit_ids(v_uid);
    -- NULL = no filter; an unknown name matches no row.
    v_filter_team INTEGER := api.usage_filter_team_id(p_team);
    v_filter_units INTEGER[] := api.usage_filter_org_unit_ids(p_org_unit, COALESCE(p_direct, FALSE));
BEGIN
    RETURN QUERY
    WITH g AS (
        SELECT u.*
        FROM api.group_daily_usage u
        WHERE u.api_key_id IS NOT NULL
          AND u.usage_date BETWEEN p_start_date AND p_end_date
          AND (p_api_key_id IS NULL OR u.api_key_id = p_api_key_id)
          AND (v_filter_team IS NULL OR v_filter_team = ANY (u.team_ids))
          AND (v_filter_units IS NULL OR u.org_unit_id = ANY (v_filter_units))
          AND (p_user_id IS NULL OR u.user_id = p_user_id)
          AND (p_service_account_id IS NULL OR u.service_account_id = p_service_account_id)
    ), keys AS MATERIALIZED (
        -- Scoped reads use group rows only, so only their keys are needed.
        SELECT
            ak.id,
            (ak.metadata).name AS key_name,
            (ak.metadata).display_name AS key_display_name,
            (ak.spec).description AS key_description,
            (ak.metadata).workspace AS key_workspace,
            api.can_read_key_usage(ak.user_id, ak.service_account_id, (ak.metadata).workspace) AS readable
        FROM api.api_keys ak
        WHERE (p_api_key_id IS NULL OR ak.id = p_api_key_id)
          AND (p_user_id IS NULL OR ak.user_id = p_user_id)
          AND (p_service_account_id IS NULL OR ak.service_account_id = p_service_account_id)
          AND (NOT v_scoped OR ak.id IN (SELECT g.api_key_id FROM g))
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
            NULL::bigint AS c_tokens,
            NULL::bigint AS cr_tokens,
            NULL::bigint AS cc_tokens,
            NULL::bigint AS r_tokens,
            NULL::numeric AS cost
        FROM api.api_daily_usage u
        JOIN keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).dimensional_usage) kv
        WHERE NOT v_scoped
          AND k.readable IS TRUE
          AND (u.spec).usage_date BETWEEN p_start_date AND p_end_date
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
            (kv.value->>'completion')::bigint AS c_tokens,
            (kv.value->>'cache_read')::bigint AS cr_tokens,
            (kv.value->>'cache_creation')::bigint AS cc_tokens,
            (kv.value->>'reasoning')::bigint AS r_tokens,
            (kv.value->>'cost_usd')::numeric AS cost
        FROM api.api_daily_usage u
        JOIN keys k ON (u.spec).api_key_id = k.id,
             jsonb_each((u.spec).detailed_dimensional_usage) kv
        WHERE NOT v_scoped
          AND k.readable IS TRUE
          AND (u.spec).usage_date BETWEEN p_start_date AND p_end_date
          AND (u.spec).detailed_dimensional_usage IS NOT NULL
    ), deleted_keys AS MATERIALIZED (
        -- A key's rows share one owner and workspace: checked once per key.
        SELECT d.api_key_id
        FROM (SELECT DISTINCT ON (g.api_key_id) g.api_key_id, g.user_id, g.service_account_id, g.workspace
              FROM g) d
        WHERE NOT EXISTS (SELECT 1 FROM keys k WHERE k.id = d.api_key_id)
          AND api.can_read_key_usage(d.user_id, d.service_account_id, d.workspace)
    ), group_data AS (
        SELECT
            g.usage_date,
            g.api_key_id,
            k.key_name,
            k.key_display_name,
            k.key_description,
            NULLIF(g.endpoint_type, '') AS endpoint_type,
            g.endpoint_name,
            NULLIF(g.model_name, '') AS model_name,
            g.workspace,
            SUM(g.total_tokens)::bigint AS dimension_usage,
            SUM(g.prompt_tokens)::bigint AS p_tokens,
            SUM(g.completion_tokens)::bigint AS c_tokens,
            SUM(g.cache_read_tokens)::bigint AS cr_tokens,
            SUM(g.cache_creation_tokens)::bigint AS cc_tokens,
            SUM(g.reasoning_tokens)::bigint AS r_tokens,
            SUM(g.cost_usd) AS cost
        FROM g
        LEFT JOIN keys k ON k.id = g.api_key_id
        WHERE CASE
            -- Unscoped, directly readable keys come from api_daily_usage above.
            WHEN k.readable IS TRUE THEN v_scoped
            ELSE g.api_key_id IN (SELECT dk.api_key_id FROM deleted_keys dk)
                 OR g.team_ids && v_teams
                 OR COALESCE(g.org_unit_id = ANY (v_units), FALSE)
        END
        GROUP BY g.usage_date, g.api_key_id, k.key_name, k.key_display_name, k.key_description,
                 g.endpoint_type, g.endpoint_name, g.model_name, g.workspace
    ), dimension_data AS (
        SELECT * FROM old_dimension_data
        UNION ALL
        SELECT * FROM new_dimension_data
        UNION ALL
        SELECT * FROM group_data
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
        d.c_tokens,
        d.cr_tokens,
        d.cc_tokens,
        d.r_tokens,
        d.cost
    FROM dimension_data d
    WHERE (p_endpoint_name IS NULL OR d.endpoint_name = p_endpoint_name)
      AND (p_workspace IS NULL OR d.workspace = p_workspace)
    ORDER BY d.usage_date DESC, d.api_key_id, d.endpoint_name;
END;
$$;
