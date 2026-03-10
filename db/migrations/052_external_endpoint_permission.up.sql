-- Add external_endpoint permissions to permission_action enum
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'external_endpoint:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'external_endpoint:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'external_endpoint:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'external_endpoint:delete';
