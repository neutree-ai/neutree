-- Revert access control policies (072).
DROP FUNCTION IF EXISTS api.get_api_key_access(UUID);
DROP FUNCTION IF EXISTS api.delete_access_policy(BIGINT);
DROP FUNCTION IF EXISTS api.get_access_policies(TEXT, TEXT, UUID, UUID);
DROP FUNCTION IF EXISTS api.set_access_policy(TEXT, TEXT, JSONB, TEXT, UUID, UUID);
DROP TABLE IF EXISTS api.access_policies;

NOTIFY pgrst, 'reload schema';
