BEGIN;
DROP FUNCTION IF EXISTS api.group_projects(TEXT, TEXT, TEXT);
NOTIFY pgrst, 'reload schema';
COMMIT;
