DROP FUNCTION IF EXISTS api.visibility(api.model_registries);

ALTER TYPE api.model_registry_status DROP ATTRIBUTE IF EXISTS last_checked_at;

NOTIFY pgrst, 'reload schema';
