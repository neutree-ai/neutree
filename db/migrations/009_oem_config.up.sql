-- ----------------------
-- Resource: OEM Configuration
-- ----------------------
CREATE TYPE api.oem_config_spec AS (
    brand_name TEXT,
    logo_base64 TEXT,
    logo_collapsed_base64 TEXT
);

CREATE TABLE api.oem_configs (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.oem_config_spec
);

CREATE TRIGGER update_oem_configs_update_timestamp
    BEFORE UPDATE ON api.oem_configs
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_oem_configs_default_timestamp
    BEFORE INSERT ON api.oem_configs
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

-- Add constraint to ensure only one OEM config exists globally
CREATE UNIQUE INDEX oem_configs_global_unique_idx
    ON api.oem_configs ((TRUE));

ALTER TABLE api.oem_configs ENABLE ROW LEVEL SECURITY;

CREATE POLICY "oem_config read policy" ON api.oem_configs
    FOR SELECT
    USING (auth.role() = 'api_user');

CREATE POLICY "oem_config create policy" ON api.oem_configs
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'system:admin', NULL)
    );

CREATE POLICY "oem_config update policy" ON api.oem_configs
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'system:admin', NULL)
    );

CREATE POLICY "oem_config delete policy" ON api.oem_configs
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'system:admin', NULL)
    );

CREATE TRIGGER validate_name_on_oem_configs
    BEFORE INSERT OR UPDATE ON api.oem_configs
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();
