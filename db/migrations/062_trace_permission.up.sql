-- Resource-scoped trace-read permissions for the AI inference trace endpoints
-- (/api/v1/ai-traces/...): one for internal endpoints, one for external. The
-- enterprise edition's has_permission override makes these workspace-scoped; in
-- community they are global (community deliberately omits per-workspace trace
-- control). Granting happens in 063, since a newly added enum value cannot be
-- referenced in the same transaction that adds it.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'endpoint:trace-read';
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'external_endpoint:trace-read';
