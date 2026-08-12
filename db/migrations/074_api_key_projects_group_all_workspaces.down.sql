-- Restore the workspace-scoped group_projects from 072/073 (no workspace
-- column, p_workspace required).

DROP FUNCTION IF EXISTS api.group_projects(TEXT, TEXT, TEXT);

CREATE OR REPLACE FUNCTION api.group_projects(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_status TEXT DEFAULT NULL
) RETURNS TABLE (
    id UUID,
    name TEXT,
    description TEXT,
    status TEXT,
    is_default BOOLEAN,
    api_key_count BIGINT,
    usage_used BIGINT,
    usage_limit BIGINT,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
)
LANGUAGE plpgsql STABLE SECURITY DEFINER
AS $$
BEGIN
    IF NOT api.has_permission(auth.uid(), 'workspace:read', p_workspace) THEN
        RAISE EXCEPTION 'permission denied';
    END IF;

    RETURN QUERY
    SELECT
        p.id,
        p.name,
        p.description,
        p.status,
        p.is_default,
        count(k.id)::bigint AS api_key_count,
        COALESCE(sum(u.used), 0)::bigint AS usage_used,
        COALESCE(sum(lim.token_limit), 0)::bigint AS usage_limit,
        p.created_at,
        p.updated_at
    FROM api.projects p
    LEFT JOIN api.api_keys k
        ON k.project_id = p.id
       AND (k.metadata).deletion_timestamp IS NULL
    LEFT JOIN LATERAL (
        SELECT
            ((k.spec).limits #>> '{token_quota,limit}')::bigint AS token_limit,
            CASE COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly')
                WHEN 'daily'   THEN CURRENT_DATE
                WHEN 'weekly'  THEN date_trunc('week',  CURRENT_DATE)::date
                WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                WHEN 'yearly'  THEN date_trunc('year',  CURRENT_DATE)::date
                ELSE date_trunc('month', CURRENT_DATE)::date
            END AS period_start
    ) lim ON TRUE
    LEFT JOIN LATERAL (
        SELECT COALESCE(SUM((d.spec).total_usage), 0)::bigint AS used
        FROM api.api_daily_usage d
        WHERE (d.spec).api_key_id = k.id
          AND (d.spec).usage_date >= lim.period_start
          AND (d.spec).usage_date <= CURRENT_DATE
    ) u ON TRUE
    WHERE p.workspace = p_workspace
      AND (p_status IS NULL OR p.status = p_status)
      AND (
          p_search IS NULL
          OR btrim(p_search) = ''
          OR p.name ILIKE '%' || p_search || '%'
          OR COALESCE(p.description, '') ILIKE '%' || p_search || '%'
          OR EXISTS (
              SELECT 1 FROM api.api_keys k2
              WHERE k2.project_id = p.id
                AND (k2.metadata).deletion_timestamp IS NULL
                AND (
                    (k2.metadata).name ILIKE '%' || p_search || '%'
                    OR COALESCE(k2.description, '') ILIKE '%' || p_search || '%'
                    OR (k2.metadata).workspace ILIKE '%' || p_search || '%'
                )
          )
      )
    GROUP BY p.id
    ORDER BY p.is_default DESC, p.created_at ASC, p.name ASC;
END;
$$;
