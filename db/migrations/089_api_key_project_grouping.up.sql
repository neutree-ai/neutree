BEGIN;

-- Batched Project -> APIKey rows. The RPC keeps pagination and aggregation in
-- the database so the UI never needs one request per Project.
CREATE OR REPLACE FUNCTION api.group_projects(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_status TEXT DEFAULT NULL
) RETURNS TABLE (
    id UUID,
    workspace TEXT,
    name TEXT,
    description TEXT,
    status TEXT,
    is_default BOOLEAN,
    api_key_count BIGINT,
    usage_used BIGINT,
    usage_limit BIGINT,
    api_keys JSONB,
    created_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ
)
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT p.id, p.workspace, p.name, p.description, p.status, p.is_default,
           count(k.id)::bigint,
           COALESCE(sum((d.spec).total_usage), 0)::bigint,
           COALESCE(sum(((k.spec).limits #>> '{token_quota,limit}')::bigint), 0)::bigint,
           COALESCE(jsonb_agg(jsonb_build_object(
               'id', k.id, 'name', (k.metadata).name,
               'description', k.description, 'workspace', (k.metadata).workspace,
               'status', (k.status).phase, 'spec', k.spec, 'created_at', (k.metadata).creation_timestamp
           ) ORDER BY (k.metadata).name) FILTER (WHERE k.id IS NOT NULL), '[]'::jsonb),
           p.created_at, p.updated_at
    FROM api.projects p
    LEFT JOIN api.api_keys k ON k.project_id = p.id
        AND (k.metadata).deletion_timestamp IS NULL
    LEFT JOIN api.api_daily_usage d ON (d.spec).api_key_id = k.id
        AND (d.spec).usage_date >= date_trunc('month', CURRENT_DATE)::date
    WHERE p.workspace = p_workspace
      AND api.has_permission(auth.uid(), 'project:read', p_workspace)
      AND (p_status IS NULL OR p.status = p_status)
      AND (p_search IS NULL OR btrim(p_search) = ''
           OR p.name ILIKE '%' || p_search || '%'
           OR COALESCE(p.description, '') ILIKE '%' || p_search || '%'
           OR (k.metadata).name ILIKE '%' || p_search || '%'
           OR COALESCE(k.description, '') ILIKE '%' || p_search || '%')
    GROUP BY p.id
    ORDER BY p.is_default DESC, p.created_at ASC, p.name ASC
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
