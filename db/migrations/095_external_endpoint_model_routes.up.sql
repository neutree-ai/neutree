-- Model-scoped routing is stored in the existing composite ExternalEndpoint
-- spec. The legacy upstream model_mapping remains unchanged and continues to
-- be accepted when model_routes is absent.
CREATE TYPE api.external_endpoint_model_route_target AS (
    upstream TEXT,
    upstream_model TEXT,
    priority INTEGER,
    weight INTEGER,
    max_inflight_requests INTEGER
);

CREATE TYPE api.external_endpoint_model_route AS (
    model TEXT,
    retryable_conditions TEXT[],
    max_attempts INTEGER,
    targets api.external_endpoint_model_route_target[]
);

ALTER TYPE api.external_endpoint_upstream_entry ADD ATTRIBUTE name TEXT;
ALTER TYPE api.external_endpoint_spec ADD ATTRIBUTE model_routes api.external_endpoint_model_route[];
