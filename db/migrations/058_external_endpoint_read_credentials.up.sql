-- Add read-credentials permission for external endpoints
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'external_endpoint:read-credentials';
