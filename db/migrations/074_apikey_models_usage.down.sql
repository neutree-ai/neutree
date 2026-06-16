-- Revert 074.
DROP FUNCTION IF EXISTS api.get_api_keys_usage_summary(TEXT);
DROP FUNCTION IF EXISTS api.get_workspace_models(TEXT);

NOTIFY pgrst, 'reload schema';
