ALTER TYPE api.external_endpoint_spec DROP ATTRIBUTE IF EXISTS model_routes;
ALTER TYPE api.external_endpoint_upstream_entry DROP ATTRIBUTE IF EXISTS name;
DROP TYPE IF EXISTS api.external_endpoint_model_route;
DROP TYPE IF EXISTS api.external_endpoint_model_route_target;
