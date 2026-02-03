-- Remove external_endpoint permissions from preset roles
UPDATE api.roles
SET spec = jsonb_set(
    spec::jsonb,
    '{permissions}',
    (
        SELECT jsonb_agg(elem)
        FROM jsonb_array_elements(spec::jsonb->'permissions') AS elem
        WHERE elem::text NOT LIKE '%external_endpoint%'
    )
)::api.role_spec
WHERE (metadata).name IN ('admin', 'workspace-admin', 'workspace-user');

-- Drop RLS policies
DROP POLICY IF EXISTS "external_endpoint read policy" ON api.external_endpoints;
DROP POLICY IF EXISTS "external_endpoint create policy" ON api.external_endpoints;
DROP POLICY IF EXISTS "external_endpoint update policy" ON api.external_endpoints;
DROP POLICY IF EXISTS "external_endpoint delete policy" ON api.external_endpoints;

-- Drop table
DROP TABLE IF EXISTS api.external_endpoints;

-- Drop types
DROP TYPE IF EXISTS api.external_endpoint_status;
DROP TYPE IF EXISTS api.external_endpoint_spec;
DROP TYPE IF EXISTS api.external_endpoint_auth_spec;
DROP TYPE IF EXISTS api.external_endpoint_upstream_spec;
