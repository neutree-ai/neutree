-- ----------------------
-- Resource: Model Catalog
-- ----------------------
CREATE TYPE api.model_catalog_spec AS (
    model api.model_spec,
    engine api.endpoint_engine_spec,
    resources api.resource_spec,
    replicas api.replica_spec,
    deployment_options json,
    variables json
);

CREATE TYPE api.model_catalog_status AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT
);

CREATE TABLE api.model_catalogs (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.model_catalog_spec,
    status api.model_catalog_status
);

CREATE TRIGGER update_model_catalogs_update_timestamp
    BEFORE UPDATE ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_model_catalogs_default_timestamp
    BEFORE INSERT ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE UNIQUE INDEX model_catalogs_name_workspace_unique_idx ON api.model_catalogs (((metadata).workspace), ((metadata).name));

ALTER TABLE api.model_catalogs ENABLE ROW LEVEL SECURITY;

CREATE POLICY "model_catalog read policy" ON api.model_catalogs
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'model_catalog:read', (metadata).workspace)
    );

CREATE POLICY "model_catalog create policy" ON api.model_catalogs
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'model_catalog:create', (metadata).workspace)
    );

CREATE POLICY "model_catalog update policy" ON api.model_catalogs
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'model_catalog:update', (metadata).workspace)
    );

CREATE POLICY "model_catalog delete policy" ON api.model_catalogs
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'model_catalog:delete', (metadata).workspace)
    );

CREATE TRIGGER validate_name_on_model_catalogs
    BEFORE INSERT OR UPDATE ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_workspace_on_model_catalogs
    BEFORE INSERT OR UPDATE ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();

-- ----------------------
-- Update admin role permissions to include new model_catalog permissions
-- ----------------------
SELECT api.update_admin_permissions();