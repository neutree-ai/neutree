-- NEU-656: permissions for the IdentitySource resource (migration 105).
-- A separate migration because enum values added here cannot be referenced in
-- the transaction that adds them.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'identity_source:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'identity_source:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'identity_source:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'identity_source:delete';
