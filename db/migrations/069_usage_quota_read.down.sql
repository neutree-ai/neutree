DROP FUNCTION IF EXISTS api.get_quota_scope_usage(TEXT, TEXT, TEXT, UUID, UUID);
DROP FUNCTION IF EXISTS api.get_quota_policies(TEXT, TEXT, UUID, UUID);

NOTIFY pgrst, 'reload schema';
