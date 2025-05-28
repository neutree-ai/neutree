-- ----------------------
-- Resource: Model Catalog - Down Migration
-- ----------------------

-- Drop triggers first
DROP TRIGGER IF EXISTS validate_workspace_on_model_catalogs ON api.model_catalogs;
DROP TRIGGER IF EXISTS validate_name_on_model_catalogs ON api.model_catalogs;

-- Drop policies
DROP POLICY IF EXISTS "model_catalog delete policy" ON api.model_catalogs;
DROP POLICY IF EXISTS "model_catalog update policy" ON api.model_catalogs;
DROP POLICY IF EXISTS "model_catalog create policy" ON api.model_catalogs;
DROP POLICY IF EXISTS "model_catalog read policy" ON api.model_catalogs;

-- Disable row level security
ALTER TABLE api.model_catalogs DISABLE ROW LEVEL SECURITY;

-- Drop index
DROP INDEX IF EXISTS model_catalogs_name_workspace_unique_idx;

-- Drop remaining triggers
DROP TRIGGER IF EXISTS set_model_catalogs_default_timestamp ON api.model_catalogs;
DROP TRIGGER IF EXISTS update_model_catalogs_update_timestamp ON api.model_catalogs;

-- Drop table
DROP TABLE IF EXISTS api.model_catalogs;

-- Drop types
DROP TYPE IF EXISTS api.model_catalog_status;
DROP TYPE IF EXISTS api.model_catalog_spec;


-- ----------------------
-- Update admin role permissions to include new model_catalog permissions
-- ----------------------
SELECT api.update_admin_permissions();