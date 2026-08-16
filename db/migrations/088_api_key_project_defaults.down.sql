BEGIN;
DROP FUNCTION IF EXISTS api.list_api_key_projects(TEXT);
NOTIFY pgrst, 'reload schema';
COMMIT;
