CREATE TYPE api.external_endpoint_model_route_target AS (
    upstream TEXT,
    upstream_model TEXT,
    priority INTEGER,
    weight INTEGER,
    max_inflight_requests INTEGER
);

CREATE TYPE api.external_endpoint_model_route AS (
    model TEXT,
    strategy TEXT,
    targets api.external_endpoint_model_route_target[]
);

ALTER TYPE api.external_endpoint_upstream_entry ADD ATTRIBUTE name TEXT;
ALTER TYPE api.external_endpoint_spec ADD ATTRIBUTE model_routes api.external_endpoint_model_route[];
