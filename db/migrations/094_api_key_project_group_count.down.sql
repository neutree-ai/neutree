BEGIN;

DROP FUNCTION IF EXISTS api.count_api_key_project_group_api_keys(
    TEXT, TEXT, BOOLEAN, BOOLEAN
);

NOTIFY pgrst, 'reload schema';
COMMIT;
