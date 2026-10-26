BEGIN;

CREATE OR REPLACE FUNCTION api.get_api_key_project_groups(
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
    WITH folders AS (
        SELECT to_jsonb(p) AS project, p.id AS project_id,
               (p.metadata).workspace AS workspace,
               (p.metadata).creation_timestamp AS created_at
        FROM api.api_key_projects p
        WHERE p.user_id = auth.uid()
          AND (p_workspace IS NULL OR (p.metadata).workspace = p_workspace)
          AND api.has_permission(auth.uid(), 'workspace:read', (p.metadata).workspace)
        UNION ALL
        SELECT jsonb_build_object(
                   'id', '__ungrouped__:' || key_workspaces.workspace,
                   'api_version', 'v1',
                   'kind', 'ApiKeyProject',
                   'metadata', jsonb_build_object(
                       'workspace', key_workspaces.workspace,
                       'name', 'Ungrouped',
                       'display_name', NULL,
                       'deletion_timestamp', NULL,
                       'creation_timestamp', NULL,
                       'update_timestamp', NULL,
                       'labels', jsonb_build_object(),
                       'annotations', jsonb_build_object()
                   ),
                   'spec', jsonb_build_object('description', ''),
                   'user_id', auth.uid(),
                   'is_ungrouped', true
               ),
               NULL::uuid,
               key_workspaces.workspace,
               '-infinity'::timestamptz
        FROM (
            SELECT p_workspace AS workspace
            WHERE p_workspace IS NOT NULL
            UNION
            SELECT (w.metadata).name AS workspace
            FROM api.workspaces w
            WHERE p_workspace IS NULL
              AND (w.metadata).deletion_timestamp IS NULL
              AND api.has_permission(auth.uid(), 'workspace:read', (w.metadata).name)
        ) key_workspaces
        WHERE api.has_permission(auth.uid(), 'workspace:read', key_workspaces.workspace)
    ), matching AS (
        SELECT f.*,
               NULLIF(trim(p_search), '') IS NULL
                   OR f.project #>> '{metadata,name}' ILIKE api.api_key_search_pattern(p_search) AS project_matches
        FROM folders f
        WHERE (
            p_api_key_disabled IS NULL
            OR EXISTS (
                SELECT 1 FROM api.api_keys status_key
                WHERE status_key.user_id = auth.uid()
                  AND (status_key.metadata).workspace = f.workspace
                  AND (status_key.spec).project_id IS NOT DISTINCT FROM f.project_id
                  AND (status_key.metadata).deletion_timestamp IS NULL
                  AND COALESCE(
                      ((status_key.spec).limits ->> 'disabled')::boolean,
                      false
                  ) = p_api_key_disabled
            )
        )
        AND (
           NULLIF(trim(p_search), '') IS NULL
           OR f.project #>> '{metadata,name}' ILIKE api.api_key_search_pattern(p_search)
           OR EXISTS (
               SELECT 1 FROM api.api_keys k
               WHERE k.user_id = auth.uid()
                 AND (k.metadata).workspace = f.workspace
                 AND (k.spec).project_id IS NOT DISTINCT FROM f.project_id
                 AND (k.metadata).deletion_timestamp IS NULL
                 AND (
                     COALESCE((k.metadata).display_name, (k.metadata).name)
                         ILIKE api.api_key_search_pattern(p_search)
                     OR (k.spec).description ILIKE api.api_key_search_pattern(p_search)
                 )
           ))
    ), visible AS (
        SELECT m.*, count(*) OVER () AS total_projects
        FROM matching m
        ORDER BY m.created_at, m.project_id NULLS FIRST
        OFFSET GREATEST(p_page - 1, 0) * LEAST(GREATEST(p_page_size, 1), 100)
        LIMIT LEAST(GREATEST(p_page_size, 1), 100)
    )
    SELECT v.project,
           COALESCE(
               jsonb_agg(
                   to_jsonb(k) #- '{status,sk_value}'
                   ORDER BY (k.metadata).creation_timestamp
               )
                   FILTER (WHERE k.id IS NOT NULL),
               '[]'::jsonb
           ),
           count(k.id),
           COALESCE(sum((k.status).usage), 0)::bigint,
           v.total_projects
    FROM visible v
    LEFT JOIN api.api_keys k
      ON k.user_id = auth.uid()
     AND (k.metadata).workspace = v.workspace
     AND (k.spec).project_id IS NOT DISTINCT FROM v.project_id
     AND (k.metadata).deletion_timestamp IS NULL
     AND (
         p_api_key_disabled IS NULL
         OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
             = p_api_key_disabled
     )
     AND (
         v.project_matches
         OR COALESCE((k.metadata).display_name, (k.metadata).name)
             ILIKE api.api_key_search_pattern(p_search)
         OR (k.spec).description ILIKE api.api_key_search_pattern(p_search)
     )
    GROUP BY v.project, v.workspace, v.created_at, v.project_id, v.total_projects
    ORDER BY v.workspace, v.created_at, v.project_id NULLS FIRST;
$$;

CREATE OR REPLACE FUNCTION api.count_api_key_project_group_api_keys(
    p_workspace TEXT,
    p_search TEXT DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL
) RETURNS BIGINT LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT count(*)
    FROM api.api_keys k
    LEFT JOIN api.api_key_projects p ON p.id = (k.spec).project_id
    WHERE k.user_id = auth.uid()
      AND (k.metadata).deletion_timestamp IS NULL
      AND (p_workspace IS NULL OR (k.metadata).workspace = p_workspace)
      AND api.has_permission(
          auth.uid(), 'workspace:read', (k.metadata).workspace
      )
      AND (
          p_api_key_disabled IS NULL
          OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
              = p_api_key_disabled
      )
      AND (
          NULLIF(trim(p_search), '') IS NULL
          OR (p.metadata).name ILIKE api.api_key_search_pattern(p_search)
          OR COALESCE((k.metadata).display_name, (k.metadata).name)
              ILIKE api.api_key_search_pattern(p_search)
          OR (k.spec).description ILIKE api.api_key_search_pattern(p_search)
      );
$$;

COMMIT;
