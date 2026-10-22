DROP TRIGGER validate_endpoint_kv_cache ON api.endpoints;
DROP FUNCTION api.validate_endpoint_kv_cache();
ALTER TYPE api.endpoint_status DROP ATTRIBUTE kv_cache;
ALTER TYPE api.endpoint_spec DROP ATTRIBUTE kv_cache;
