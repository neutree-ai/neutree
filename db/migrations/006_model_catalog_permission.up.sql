-- ----------------------
-- Add model_catalog permissions to permission_action enum
-- ----------------------
ALTER TYPE api.permission_action ADD VALUE 'model_catalog:read';
ALTER TYPE api.permission_action ADD VALUE 'model_catalog:create';
ALTER TYPE api.permission_action ADD VALUE 'model_catalog:update';
ALTER TYPE api.permission_action ADD VALUE 'model_catalog:delete';
