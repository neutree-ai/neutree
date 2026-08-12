BEGIN;

DROP TRIGGER IF EXISTS workspaces_ensure_default_project ON api.workspaces;
DROP FUNCTION IF EXISTS api.ensure_default_project();
DROP TRIGGER IF EXISTS api_keys_project_validation ON api.api_keys;
DROP FUNCTION IF EXISTS api.validate_api_key_project();
DROP TRIGGER IF EXISTS projects_validate_write ON api.projects;
DROP FUNCTION IF EXISTS api.validate_project_write();

DROP POLICY IF EXISTS projects_delete_policy ON api.projects;
DROP POLICY IF EXISTS projects_update_policy ON api.projects;
DROP POLICY IF EXISTS projects_insert_policy ON api.projects;
DROP POLICY IF EXISTS projects_read_policy ON api.projects;

ALTER TABLE api.api_keys DROP CONSTRAINT IF EXISTS api_keys_project_fk;
DROP INDEX IF EXISTS api.api_keys_project_idx;
DROP INDEX IF EXISTS api.api_key_name_project_unique_idx;
CREATE UNIQUE INDEX api_key_name_workspace_unique_idx
    ON api.api_keys (((metadata).workspace), ((metadata).name));
ALTER TABLE api.api_keys DROP COLUMN IF EXISTS project_id;
ALTER TABLE api.api_keys DROP COLUMN IF EXISTS description;
DROP TABLE IF EXISTS api.projects;

NOTIFY pgrst, 'reload schema';
COMMIT;
