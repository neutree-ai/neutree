-- Add the trace:read permission to the permission_action enum.
-- Gates the AI inference trace endpoints (/api/v1/ai-traces/...), which were
-- previously guarded by the generic workspace:read. Granting happens in 063,
-- since a newly added enum value cannot be referenced in the same transaction.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'trace:read';
