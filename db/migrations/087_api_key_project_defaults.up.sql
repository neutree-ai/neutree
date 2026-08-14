BEGIN;

-- Every user gets a Default API key project in every active workspace.
INSERT INTO api.api_key_projects (name, description, workspace, user_id)
SELECT 'Default', 'Default API key project', (w.metadata).name, u.id
FROM api.user_profiles u
CROSS JOIN api.workspaces w
WHERE (w.metadata).deletion_timestamp IS NULL
ON CONFLICT (user_id, workspace, name) DO NOTHING;

CREATE OR REPLACE FUNCTION api.create_default_api_key_projects_for_user()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER AS $$
BEGIN
    INSERT INTO api.api_key_projects (name, description, workspace, user_id)
    SELECT 'Default', 'Default API key project', (w.metadata).name, NEW.id
    FROM api.workspaces w
    WHERE (w.metadata).deletion_timestamp IS NULL
    ON CONFLICT (user_id, workspace, name) DO NOTHING;
    RETURN NEW;
END;
$$;

CREATE TRIGGER create_default_api_key_projects_for_user
    AFTER INSERT ON api.user_profiles
    FOR EACH ROW EXECUTE FUNCTION api.create_default_api_key_projects_for_user();

CREATE OR REPLACE FUNCTION api.create_default_api_key_projects_for_workspace()
RETURNS TRIGGER
LANGUAGE plpgsql SECURITY DEFINER AS $$
BEGIN
    INSERT INTO api.api_key_projects (name, description, workspace, user_id)
    SELECT 'Default', 'Default API key project', (NEW.metadata).name, u.id
    FROM api.user_profiles u
    ON CONFLICT (user_id, workspace, name) DO NOTHING;
    RETURN NEW;
END;
$$;

CREATE TRIGGER create_default_api_key_projects_for_workspace
    AFTER INSERT ON api.workspaces
    FOR EACH ROW EXECUTE FUNCTION api.create_default_api_key_projects_for_workspace();

-- Project pickers and detail pages use an RPC because direct table access is
-- intentionally not exposed through PostgREST.
CREATE OR REPLACE FUNCTION api.list_api_key_projects(p_workspace TEXT)
RETURNS SETOF api.api_key_projects
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT p.*
    FROM api.api_key_projects p
    WHERE p.user_id = auth.uid()
      AND p.workspace = p_workspace
    ORDER BY (p.name <> 'Default'), p.created_at, p.id;
$$;

NOTIFY pgrst, 'reload schema';
COMMIT;
