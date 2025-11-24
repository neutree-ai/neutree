------------------------------------
-- Rollback Whitespace Validation
------------------------------------

DROP TRIGGER IF EXISTS validate_engine_no_whitespace_trigger ON api.engines;
DROP FUNCTION IF EXISTS api.validate_engine_no_whitespace();

DROP TRIGGER IF EXISTS validate_cluster_no_whitespace_trigger ON api.clusters;
DROP FUNCTION IF EXISTS api.validate_cluster_no_whitespace();

DROP TRIGGER IF EXISTS validate_endpoint_no_whitespace_trigger ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_no_whitespace();

DROP TRIGGER IF EXISTS validate_model_registry_no_whitespace_trigger ON api.model_registries;
DROP FUNCTION IF EXISTS api.validate_model_registry_no_whitespace();

DROP TRIGGER IF EXISTS validate_image_registry_no_whitespace_trigger ON api.image_registries;
DROP FUNCTION IF EXISTS api.validate_image_registry_no_whitespace();
