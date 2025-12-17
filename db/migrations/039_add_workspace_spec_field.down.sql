-- ----------------------
-- Resource: Workspace
-- ----------------------
--- drop workspace_spec type and spec column from workspaces table
ALTER TABLE api.workspaces
    DROP COLUMN IF EXISTS spec;

DROP TYPE IF EXISTS api.workspace_spec;