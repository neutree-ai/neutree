-- Add read-credentials permission for external endpoints
ALTER TYPE api.permission_action ADD VALUE 'external_endpoint:read-credentials';

-- Grant to admin role (update_admin_permissions grants all enum values)
SELECT api.update_admin_permissions();
