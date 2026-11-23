-- Permissions for the global LogSink resource.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'log_sink:read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'log_sink:create';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'log_sink:update';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'log_sink:delete';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'log_sink:read-credentials';
