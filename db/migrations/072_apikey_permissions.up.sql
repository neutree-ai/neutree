-- Add api_key limit read/write permissions to the permission_action enum.
--
-- API-key limits (token quota + access limits, incl. disable) were only
-- owner-scoped (RLS user_id = auth.uid()) with no RBAC permission. Add
-- api_key:read / api_key:update so reading and editing a key's limits can be
-- governed by roles (e.g. a read-only member can view but not change limits).
-- Enum values must be committed before they can be referenced by grants /
-- has_permission, so the grants + RPC enforcement live in the next migration (073).
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'api_key:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'api_key:update';
