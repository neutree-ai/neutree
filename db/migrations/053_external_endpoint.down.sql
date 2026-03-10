-- Remove external_endpoint permissions from preset roles
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    array_remove(
        array_remove(
            array_remove(
                array_remove((spec).permissions, 'external_endpoint:read'::api.permission_action),
                'external_endpoint:create'::api.permission_action
            ),
            'external_endpoint:update'::api.permission_action
        ),
        'external_endpoint:delete'::api.permission_action
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

-- Drop types (order matters: dependent types first)
DROP TYPE IF EXISTS api.external_endpoint_status;
DROP TYPE IF EXISTS api.external_endpoint_spec;
DROP TYPE IF EXISTS api.external_endpoint_upstream_entry;
DROP TYPE IF EXISTS api.external_endpoint_auth_spec;
DROP TYPE IF EXISTS api.external_endpoint_upstream_spec;
