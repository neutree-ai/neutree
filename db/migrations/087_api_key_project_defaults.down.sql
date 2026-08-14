BEGIN;
DROP FUNCTION IF EXISTS api.list_api_key_projects(TEXT);
DROP TRIGGER IF EXISTS create_default_api_key_projects_for_workspace ON api.workspaces;
DROP FUNCTION IF EXISTS api.create_default_api_key_projects_for_workspace();
DROP TRIGGER IF EXISTS create_default_api_key_projects_for_user ON api.user_profiles;
DROP FUNCTION IF EXISTS api.create_default_api_key_projects_for_user();
NOTIFY pgrst, 'reload schema';
COMMIT;
