DROP FUNCTION IF EXISTS api.migrate_api_keys(UUID[], UUID);
DROP TRIGGER IF EXISTS api_keys_project_validation ON api.api_keys;
DROP FUNCTION IF EXISTS api.validate_api_key_project();
DROP TRIGGER IF EXISTS projects_updated_at ON api.projects;
DROP FUNCTION IF EXISTS api.validate_project_write();
DROP FUNCTION IF EXISTS api.create_project(TEXT, TEXT, TEXT);

DROP TABLE IF EXISTS api.api_key_project_history;
DROP POLICY IF EXISTS "Project delete policy" ON api.projects;
DROP POLICY IF EXISTS "Project update policy" ON api.projects;
DROP POLICY IF EXISTS "Project create policy" ON api.projects;
DROP POLICY IF EXISTS "Project read policy" ON api.projects;
ALTER TABLE api.projects DISABLE ROW LEVEL SECURITY;

ALTER TABLE api.api_keys DROP CONSTRAINT IF EXISTS api_keys_project_fk;
DROP INDEX IF EXISTS api.api_keys_project_idx;
DROP INDEX IF EXISTS api.api_key_name_project_unique_idx;
CREATE UNIQUE INDEX api_key_name_workspace_unique_idx
    ON api.api_keys (((metadata).workspace), ((metadata).name));
ALTER TABLE api.api_keys DROP COLUMN IF EXISTS project_id;
ALTER TABLE api.api_keys DROP COLUMN IF EXISTS description;
DROP TABLE IF EXISTS api.projects;

DROP FUNCTION IF EXISTS api.create_api_key(TEXT, TEXT, INTEGER, TEXT, INTEGER, JSONB, UUID, TEXT);
