-- Whether an endpoint needs a model registry depends on the engine (does Neutree
-- download the model?) and on the caller's workspace, neither of which a trigger
-- can read. The check lives in internal/routes/proxies/endpoint_validation.go.
DROP TRIGGER IF EXISTS validate_endpoint_model_registry_on_endpoints ON api.endpoints;
DROP FUNCTION IF EXISTS api.validate_endpoint_model_registry();
