-- Permissions for Teams created in Neutree.
-- A separate migration because enum values added here cannot be referenced in
-- the transaction that adds them.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team:delete';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team:assign-member';
