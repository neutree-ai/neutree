BEGIN;

DROP TRIGGER IF EXISTS projects_reject_nonempty_delete ON api.projects;
DROP FUNCTION IF EXISTS api.reject_project_with_api_keys();
DROP TRIGGER IF EXISTS projects_validate_workspace ON api.projects;
DROP FUNCTION IF EXISTS api.validate_project_workspace();

NOTIFY pgrst, 'reload schema';
COMMIT;
