DROP TRIGGER IF EXISTS validate_endpoint_model_name_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_model_name();

DROP TRIGGER IF EXISTS validate_endpoint_model_version_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_model_version();

DROP TRIGGER IF EXISTS validate_endpoint_replica_count_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_replica_count();

DROP TRIGGER IF EXISTS validate_endpoint_cluster_name_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_cluster_name();

DROP TRIGGER IF EXISTS validate_endpoint_model_registry_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_model_registry();
