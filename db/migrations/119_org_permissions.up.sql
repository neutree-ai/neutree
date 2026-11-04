-- Permissions for OrgUnits and Teams; a separate migration because an enum
-- value cannot be used in the transaction that adds it.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'org_unit:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'org_unit:assign-member';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team:read';
