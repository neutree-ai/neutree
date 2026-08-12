ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:delete';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'project:migrate';
