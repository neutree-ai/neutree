-- Drop OEM configuration resource and related objects

-- Drop validation triggers
DROP TRIGGER IF EXISTS validate_name_on_oem_configs ON api.oem_configs;

-- Drop policies
DROP POLICY IF EXISTS "oem_config delete policy" ON api.oem_configs;
DROP POLICY IF EXISTS "oem_config update policy" ON api.oem_configs;
DROP POLICY IF EXISTS "oem_config create policy" ON api.oem_configs;
DROP POLICY IF EXISTS "oem_config read policy" ON api.oem_configs;

-- Drop timestamp triggers
DROP TRIGGER IF EXISTS set_oem_configs_default_timestamp ON api.oem_configs;
DROP TRIGGER IF EXISTS update_oem_configs_update_timestamp ON api.oem_configs;

-- Drop table
DROP TABLE IF EXISTS api.oem_configs;

-- Drop types
DROP TYPE IF EXISTS api.oem_config_spec;
