BEGIN;

-- Project pickers and detail pages use an RPC because direct table access is
-- intentionally not exposed through PostgREST.
CREATE OR REPLACE FUNCTION api.list_api_key_projects(p_workspace TEXT)
RETURNS SETOF api.api_key_projects
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT p.*
    FROM api.api_key_projects p
    WHERE p.user_id = auth.uid()
      AND p.workspace = p_workspace
    ORDER BY p.created_at, p.id;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
