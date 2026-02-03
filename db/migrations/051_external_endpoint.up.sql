-- ----------------------
-- Resource: ExternalEndpoint (v1beta1)
-- ----------------------

-- Upstream configuration for external API
CREATE TYPE api.external_endpoint_upstream_spec AS (
    url TEXT
);

-- Authentication configuration for external API
CREATE TYPE api.external_endpoint_auth_spec AS (
    type TEXT,
    credential TEXT
);

-- ExternalEndpoint spec
CREATE TYPE api.external_endpoint_spec AS (
    upstream api.external_endpoint_upstream_spec,
    auth api.external_endpoint_auth_spec,
    route_type TEXT,
    timeout INTEGER
);

-- ExternalEndpoint status
CREATE TYPE api.external_endpoint_status AS (
    phase TEXT,
    service_url TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT
);

-- ExternalEndpoint table
CREATE TABLE api.external_endpoints (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.external_endpoint_spec,
    status api.external_endpoint_status
);

-- Update timestamp trigger
CREATE TRIGGER update_external_endpoints_update_timestamp
    BEFORE UPDATE ON api.external_endpoints
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

-- Default timestamp trigger
CREATE TRIGGER set_external_endpoints_default_timestamp
    BEFORE INSERT ON api.external_endpoints
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

-- Unique index on workspace and name
CREATE UNIQUE INDEX external_endpoints_name_workspace_unique_idx ON api.external_endpoints (((metadata).workspace), ((metadata).name));

-- Enable row level security
ALTER TABLE api.external_endpoints ENABLE ROW LEVEL SECURITY;

-- RLS policies
CREATE POLICY "external_endpoint read policy" ON api.external_endpoints
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'external_endpoint:read', (metadata).workspace)
    );

CREATE POLICY "external_endpoint create policy" ON api.external_endpoints
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'external_endpoint:create', (metadata).workspace)
    );

CREATE POLICY "external_endpoint update policy" ON api.external_endpoints
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'external_endpoint:update', (metadata).workspace)
    );

CREATE POLICY "external_endpoint delete policy" ON api.external_endpoints
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'external_endpoint:delete', (metadata).workspace)
    );

-- Add external_endpoint permissions to preset roles
-- Admin role gets full permissions
UPDATE api.roles
SET spec = jsonb_set(
    spec::jsonb,
    '{permissions}',
    (spec::jsonb->'permissions')::jsonb || '["external_endpoint:read", "external_endpoint:create", "external_endpoint:update", "external_endpoint:delete"]'::jsonb
)::api.role_spec
WHERE (metadata).name = 'admin';

-- Workspace admin gets full permissions
UPDATE api.roles
SET spec = jsonb_set(
    spec::jsonb,
    '{permissions}',
    (spec::jsonb->'permissions')::jsonb || '["external_endpoint:read", "external_endpoint:create", "external_endpoint:update", "external_endpoint:delete"]'::jsonb
)::api.role_spec
WHERE (metadata).name = 'workspace-admin';

-- Workspace user gets read permission only
UPDATE api.roles
SET spec = jsonb_set(
    spec::jsonb,
    '{permissions}',
    (spec::jsonb->'permissions')::jsonb || '["external_endpoint:read"]'::jsonb
)::api.role_spec
WHERE (metadata).name = 'workspace-user';
