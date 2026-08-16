BEGIN;

CREATE OR REPLACE FUNCTION api.count_api_key_project_group_api_keys(
    p_workspace TEXT, p_search TEXT DEFAULT NULL,
    p_project_enabled BOOLEAN DEFAULT NULL,
    p_api_key_disabled BOOLEAN DEFAULT NULL
) RETURNS BIGINT
LANGUAGE sql STABLE SECURITY DEFINER AS $$
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
                  SELECT 1 FROM api.api_keys search_key
                  WHERE search_key.project_id = p.id
                    AND (search_key.metadata).deletion_timestamp IS NULL
                    AND (
                        p_api_key_disabled IS NULL
                        OR COALESCE(((search_key.spec).limits ->> 'disabled')::boolean, false)
                            = p_api_key_disabled
                    )
                    AND (
                        (search_key.metadata).name ILIKE '%' || trim(p_search) || '%'
                        OR search_key.description ILIKE '%' || trim(p_search) || '%'
                    )
              )
          )
    )
    SELECT count(k.id)
    FROM matching_projects p
    JOIN api.api_keys k ON k.project_id = p.id
      AND (k.metadata).deletion_timestamp IS NULL
      AND (
          p_api_key_disabled IS NULL
          OR COALESCE(((k.spec).limits ->> 'disabled')::boolean, false)
              = p_api_key_disabled
      )
      AND (
          p.project_matches
          OR (k.metadata).name ILIKE '%' || trim(p_search) || '%'
          OR k.description ILIKE '%' || trim(p_search) || '%'
      );
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
