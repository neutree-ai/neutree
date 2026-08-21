DROP FUNCTION IF EXISTS api.get_api_key_project_groups(TEXT, TEXT, BOOLEAN, INTEGER, INTEGER);
ALTER FUNCTION api.get_api_key_project_groups_unredacted(TEXT, TEXT, BOOLEAN, INTEGER, INTEGER)
    RENAME TO get_api_key_project_groups;
