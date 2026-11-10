-- Permissions and preset roles of group policies, OrgUnit usage, model
-- invocation, ServiceAccounts and Team usage. Enum values cannot be used in
-- the transaction that adds them, so the migrations after this one grant them.

ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'group_policy:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'group_policy:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'group_policy:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'group_policy:delete';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'org_unit:usage-read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'model:invoke';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'service_account:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'service_account:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'service_account:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'service_account:delete';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'team_usage:read';

ALTER TYPE api.role_preset ADD VALUE IF NOT EXISTS 'team-admin';
ALTER TYPE api.role_preset ADD VALUE IF NOT EXISTS 'model-invoker';
