BEGIN;

DROP TRIGGER IF EXISTS create_default_api_key_projects_for_workspace ON api.workspaces;
DROP FUNCTION IF EXISTS api.create_default_api_key_projects_for_workspace();
DROP TRIGGER IF EXISTS create_default_api_key_projects_for_user ON api.user_profiles;
DROP FUNCTION IF EXISTS api.create_default_api_key_projects_for_user();
DROP FUNCTION IF EXISTS api.list_api_key_projects(TEXT);
DROP FUNCTION IF EXISTS api.delete_api_key_project(UUID);
DROP FUNCTION IF EXISTS api.get_api_key_project_groups(TEXT, TEXT, BOOLEAN, BOOLEAN, INTEGER, INTEGER);
DROP INDEX IF EXISTS api.api_key_projects_one_default_idx;

ALTER TABLE api.api_key_projects DROP COLUMN is_default;

CREATE OR REPLACE FUNCTION api.list_api_key_projects(p_workspace TEXT)
RETURNS SETOF api.api_key_projects
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT p.*
    FROM api.api_key_projects p
    WHERE p.user_id = auth.uid()
      AND p.workspace = p_workspace
    ORDER BY p.created_at, p.id;
$$;

CREATE OR REPLACE FUNCTION api.delete_api_key_project(p_project_id UUID)
RETURNS VOID LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE v_count BIGINT;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.api_key_projects
                   WHERE id = p_project_id AND user_id = auth.uid()) THEN
        RAISE EXCEPTION 'project not found or permission denied' USING ERRCODE = '42501';
    END IF;
    SELECT count(*) INTO v_count FROM api.api_keys WHERE project_id = p_project_id;
    IF v_count > 0 THEN
        RAISE EXCEPTION 'Project has % API keys', v_count USING ERRCODE = '23503', DETAIL = v_count::text;
    END IF;
    DELETE FROM api.api_key_projects WHERE id = p_project_id;
END;
$$;

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
              p_api_key_disabled IS NULL
              OR EXISTS (
                  SELECT 1 FROM api.api_keys status_key
                  WHERE status_key.project_id = p.id
                    AND (status_key.metadata).deletion_timestamp IS NULL
                    AND COALESCE(((status_key.spec).limits ->> 'disabled')::boolean, false)
                        = p_api_key_disabled
              )
          )
          AND (
              NULLIF(trim(p_search), '') IS NULL
              OR p.name ILIKE '%' || trim(p_search) || '%'
              OR EXISTS (
                  SELECT 1 FROM api.api_keys k
                  WHERE k.project_id = p.id
                    AND (k.metadata).deletion_timestamp IS NULL
                    AND (
                        p_api_key_disabled IS NULL
                        OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
                            = p_api_key_disabled
                    )
                    AND ((k.metadata).name ILIKE '%' || trim(p_search) || '%'
                         OR k.description ILIKE '%' || trim(p_search) || '%')
              )
          )
    ), visible_projects AS (
        SELECT p.*, count(*) OVER () AS total_projects
        FROM matching_projects p
        ORDER BY p.created_at, p.id
        OFFSET GREATEST(p_page - 1, 0) * LEAST(GREATEST(p_page_size, 1), 100)
        LIMIT LEAST(GREATEST(p_page_size, 1), 100)
    ), key_periods AS (
        SELECT k.id, k.project_id,
               CASE COALESCE((k.spec).limits #>> '{token_quota,period}', 'monthly')
                   WHEN 'daily' THEN CURRENT_DATE
                   WHEN 'weekly' THEN date_trunc('week', CURRENT_DATE)::date
                   WHEN 'monthly' THEN date_trunc('month', CURRENT_DATE)::date
                   WHEN 'yearly' THEN date_trunc('year', CURRENT_DATE)::date
                   ELSE date_trunc('month', CURRENT_DATE)::date
               END AS period_start
        FROM api.api_keys k
        JOIN visible_projects p ON p.id = k.project_id
        WHERE (k.metadata).deletion_timestamp IS NULL
    ), project_usage AS (
        SELECT kp.project_id,
               COALESCE(sum((d.spec).total_usage), 0)::bigint AS current_usage
        FROM key_periods kp
        LEFT JOIN api.api_daily_usage d
          ON (d.spec).api_key_id = kp.id
         AND (d.spec).usage_date >= kp.period_start
         AND (d.spec).usage_date <= CURRENT_DATE
        GROUP BY kp.project_id
    )
    SELECT ROW(p.id, p.name, p.description, p.workspace, p.enabled, p.user_id,
               p.created_at, p.updated_at)::api.api_key_projects,
           COALESCE(jsonb_agg(to_jsonb(k) ORDER BY (k.metadata).creation_timestamp)
                    FILTER (WHERE k.id IS NOT NULL), '[]'::jsonb),
           (SELECT count(*) FROM api.api_keys all_keys
            WHERE all_keys.project_id = p.id
              AND (all_keys.metadata).deletion_timestamp IS NULL),
           COALESCE(pu.current_usage, 0),
           p.total_projects
    FROM visible_projects p
    LEFT JOIN project_usage pu ON pu.project_id = p.id
    LEFT JOIN api.api_keys k ON k.project_id = p.id
      AND (k.metadata).deletion_timestamp IS NULL
      AND (p_api_key_disabled IS NULL
           OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false) = p_api_key_disabled)
      AND (p.project_matches
           OR (k.metadata).name ILIKE '%' || trim(p_search) || '%'
           OR k.description ILIKE '%' || trim(p_search) || '%')
    GROUP BY p.id, p.name, p.description, p.workspace, p.enabled, p.user_id,
             p.created_at, p.updated_at, p.project_matches, p.total_projects,
             pu.current_usage
    ORDER BY p.created_at, p.id;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
