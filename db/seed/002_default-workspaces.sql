-- ----------------------
-- Seed default workspaces
-- ----------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM api.workspaces WHERE (metadata).name = 'default') THEN
        INSERT INTO api.workspaces (api_version, kind, metadata)
        VALUES (
            'v1',
            'Workspace',
            ROW('default', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
        );
    END IF;
END
$$;
