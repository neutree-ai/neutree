-- NEU-836: permissions for the organization model (migration 109).
-- A separate migration because enum values added here cannot be referenced in
-- the transaction that adds them.
--
-- OrgUnits and Teams come only from directory sync, which writes as
-- service_role (BYPASSRLS), so there is no create/update/delete permission for
-- them. Users can only read them, and an admin can put a local account into an
-- existing OrgUnit with org_unit:assign-member.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'org_unit:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'org_unit:assign-member';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team:read';
