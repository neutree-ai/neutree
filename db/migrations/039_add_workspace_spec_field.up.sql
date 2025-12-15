-- ----------------------
-- Resource: Workspace
-- ----------------------
--- Add workspace_spec type and spec column to workspaces table
CREATE TYPE api.workspace_spec AS (
);

ALTER TABLE api.workspaces
    ADD COLUMN spec api.workspace_spec;