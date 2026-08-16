BEGIN;

CREATE OR REPLACE FUNCTION api.get_api_key_project_groups(
    p_workspace TEXT, p_search TEXT DEFAULT NULL, p_project_enabled BOOLEAN DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL, p_page INTEGER DEFAULT 1, p_page_size INTEGER DEFAULT 20
) RETURNS TABLE (
    project api.api_key_projects, api_keys JSONB, api_key_count BIGINT,
    current_usage BIGINT, total_projects BIGINT
) LANGUAGE sql STABLE SECURITY DEFINER AS $$
    WITH matching_projects AS (
        SELECT p.*,
               NULLIF(trim(p_search), '') IS NULL
                   OR p.name ILIKE '%' || trim(p_search) || '%' AS project_matches
        FROM api.api_key_projects p
        WHERE p.user_id = auth.uid()
          AND (p_workspace IS NULL OR p.workspace = p_workspace)
          AND (p_project_enabled IS NULL OR p.enabled = p_project_enabled)
          AND (
              NULLIF(trim(p_search), '') IS NULL
              OR p.name ILIKE '%' || trim(p_search) || '%'
              OR EXISTS (
                  SELECT 1 FROM api.api_keys k
                  WHERE k.project_id = p.id
                    AND ((k.metadata).name ILIKE '%' || trim(p_search) || '%'
                         OR k.description ILIKE '%' || trim(p_search) || '%'
                         OR (k.metadata).workspace ILIKE '%' || trim(p_search) || '%')
              )
          )
    ), visible_projects AS (
        SELECT p.*, count(*) OVER () AS total_projects
        FROM matching_projects p
        ORDER BY p.created_at, p.id
        OFFSET GREATEST(p_page - 1, 0) * LEAST(GREATEST(p_page_size, 1), 100)
        LIMIT LEAST(GREATEST(p_page_size, 1), 100)
    )
    SELECT ROW(p.id, p.name, p.description, p.workspace, p.enabled, p.user_id,
               p.created_at, p.updated_at)::api.api_key_projects,
           COALESCE(jsonb_agg(to_jsonb(k) ORDER BY (k.metadata).creation_timestamp)
                    FILTER (WHERE k.id IS NOT NULL), '[]'::jsonb),
           (SELECT count(*) FROM api.api_keys all_keys WHERE all_keys.project_id = p.id),
           COALESCE((SELECT sum((all_keys.status).usage)
                     FROM api.api_keys all_keys WHERE all_keys.project_id = p.id), 0)::bigint,
           p.total_projects
    FROM visible_projects p
    LEFT JOIN api.api_keys k ON k.project_id = p.id
      AND (p_api_key_disabled IS NULL
           OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false) = p_api_key_disabled)
      AND (p.project_matches
           OR (k.metadata).name ILIKE '%' || trim(p_search) || '%'
           OR k.description ILIKE '%' || trim(p_search) || '%'
           OR (k.metadata).workspace ILIKE '%' || trim(p_search) || '%')
    GROUP BY p.id, p.name, p.description, p.workspace, p.enabled, p.user_id,
             p.created_at, p.updated_at, p.project_matches, p.total_projects
    ORDER BY p.created_at, p.id;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
