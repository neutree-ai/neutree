BEGIN;
DROP FUNCTION IF EXISTS api.update_api_key_configuration(UUID, UUID, JSONB);
NOTIFY pgrst, 'reload schema';
COMMIT;
