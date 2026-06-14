-- Quota & usage control (NEUTREE-GENERAL-9): add api_key:* permissions so that
-- API-key-level quota management can reuse the "create/edit API key" permission,
-- mirroring how workspace/user quota reuse the workspace permissions. Granting
-- these to the built-in roles and every table/policy/function that references
-- the new values lives in 068, because a newly added enum value cannot be
-- referenced in the same transaction that adds it.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'api_key:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'api_key:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'api_key:delete';
