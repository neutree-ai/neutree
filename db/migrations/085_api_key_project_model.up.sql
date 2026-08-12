-- APIKey Project model and compatibility migration.
-- Existing API keys retain their IDs, secrets, status, limits and timestamps.
BEGIN;

CREATE TABLE api.projects (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'enabled'
        CHECK (status IN ('enabled', 'disabled')),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT projects_name_workspace_unique UNIQUE (workspace, name),
    CONSTRAINT projects_default_name CHECK (NOT is_default OR name = 'Default'),
    CONSTRAINT projects_default_enabled CHECK (NOT is_default OR status = 'enabled')
);

CREATE INDEX projects_workspace_idx ON api.projects (workspace);

-- Create one stable default Project for every existing workspace and every
-- legacy API key workspace before making api_keys.project_id non-null.
INSERT INTO api.projects (workspace, name, is_default)
SELECT DISTINCT workspace, 'Default', TRUE
FROM (
    SELECT (metadata).name AS workspace FROM api.workspaces
    UNION
    SELECT (metadata).workspace AS workspace FROM api.api_keys
) workspaces
WHERE workspace IS NOT NULL
ON CONFLICT (workspace, name) DO NOTHING;

ALTER TABLE api.api_keys ADD COLUMN project_id UUID;
ALTER TABLE api.api_keys ADD COLUMN description TEXT;

UPDATE api.api_keys k
SET project_id = p.id
FROM api.projects p
WHERE p.workspace = (k.metadata).workspace AND p.is_default;

ALTER TABLE api.api_keys ALTER COLUMN project_id SET NOT NULL;
ALTER TABLE api.api_keys ADD CONSTRAINT api_keys_project_fk
    FOREIGN KEY (project_id) REFERENCES api.projects(id);

DROP INDEX IF EXISTS api.api_key_name_workspace_unique_idx;
CREATE UNIQUE INDEX api_key_name_project_unique_idx
    ON api.api_keys (project_id, ((metadata).name));
CREATE INDEX api_keys_project_idx ON api.api_keys (project_id);

ALTER TABLE api.projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY projects_read_policy ON api.projects FOR SELECT USING (
    api.has_permission(auth.uid(), 'workspace:read', workspace)
);
CREATE POLICY projects_insert_policy ON api.projects FOR INSERT WITH CHECK (
    api.has_permission(auth.uid(), 'workspace:create', workspace)
);
CREATE POLICY projects_update_policy ON api.projects FOR UPDATE USING (
    api.has_permission(auth.uid(), 'workspace:update', workspace)
);
CREATE POLICY projects_delete_policy ON api.projects FOR DELETE USING (
    NOT is_default AND api.has_permission(auth.uid(), 'workspace:delete', workspace)
);

CREATE OR REPLACE FUNCTION api.validate_project_write()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.name IS NULL OR btrim(NEW.name) = '' THEN
        RAISE EXCEPTION 'Project name is required';
    END IF;
    IF NEW.is_default AND (NEW.name <> 'Default' OR NEW.status <> 'enabled') THEN
        RAISE EXCEPTION 'Default Project cannot be renamed or disabled';
    END IF;
    NEW.name := btrim(NEW.name);
    NEW.updated_at := CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER projects_validate_write
    BEFORE INSERT OR UPDATE ON api.projects
    FOR EACH ROW EXECUTE FUNCTION api.validate_project_write();

CREATE OR REPLACE FUNCTION api.validate_api_key_project()
RETURNS TRIGGER AS $$
DECLARE
    v_project api.projects;
BEGIN
    SELECT * INTO v_project FROM api.projects WHERE id = NEW.project_id;
    IF NOT FOUND OR v_project.workspace <> (NEW.metadata).workspace THEN
        RAISE EXCEPTION 'Project must belong to the API key workspace';
    END IF;
    IF TG_OP = 'INSERT' AND v_project.status <> 'enabled' THEN
        RAISE EXCEPTION 'Project is disabled';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER api_keys_project_validation
    BEFORE INSERT OR UPDATE OF project_id, metadata ON api.api_keys
    FOR EACH ROW EXECUTE FUNCTION api.validate_api_key_project();

CREATE OR REPLACE FUNCTION api.ensure_default_project()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO api.projects (workspace, name, is_default)
    VALUES ((NEW.metadata).name, 'Default', TRUE)
    ON CONFLICT (workspace, name) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

CREATE TRIGGER workspaces_ensure_default_project
    AFTER INSERT ON api.workspaces
    FOR EACH ROW EXECUTE FUNCTION api.ensure_default_project();

NOTIFY pgrst, 'reload schema';
COMMIT;
