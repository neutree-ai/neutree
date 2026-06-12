-- Workspace-scoped usage-read permission for the model-usage analytics RPC
-- (api.get_usage_by_dimension). The enterprise edition's has_permission
-- override makes this workspace-scoped, so a member granted it in a given
-- workspace can read every API key's usage in that workspace; in community it
-- degrades to a global check (community has no per-workspace usage control).
-- Granting happens in 065, since a newly added enum value cannot be referenced
-- in the same transaction that adds it.
ALTER TYPE api.permission_action ADD VALUE IF NOT EXISTS 'workspace:usage-read';
