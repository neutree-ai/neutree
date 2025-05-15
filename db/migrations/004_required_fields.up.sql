-- ----------------------
-- Create validation function for required metadata.name
-- ----------------------
CREATE OR REPLACE FUNCTION api.validate_metadata_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.metadata).name IS NULL OR (NEW.metadata).name = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10001","message": "metadata.name is required for the resource","hint": "Please provide a valid name in the metadata object"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    -- Validate name format (Kubernetes-style naming convention)
    IF NOT (NEW.metadata).name ~ '^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10003","message": "Invalid metadata.name format","hint": "Name must consist of lowercase alphanumeric characters, ''-'' or ''.'', must start and end with an alphanumeric character"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    -- Validate maximum length
    IF length((NEW.metadata).name) > 63 THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10004","message": "metadata.name is too long","hint": "Name cannot exceed 63 characters"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Add triggers for all resource tables to validate metadata.name
CREATE TRIGGER validate_name_on_workspaces
    BEFORE INSERT OR UPDATE ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_roles
    BEFORE INSERT OR UPDATE ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_role_assignments
    BEFORE INSERT OR UPDATE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_api_keys
    BEFORE INSERT OR UPDATE ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_image_registries
    BEFORE INSERT OR UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_model_registries
    BEFORE INSERT OR UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_engines
    BEFORE INSERT OR UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_clusters
    BEFORE INSERT OR UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_user_profiles
    BEFORE INSERT OR UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

CREATE TRIGGER validate_name_on_api_daily_usage
    BEFORE INSERT OR UPDATE ON api.api_daily_usage
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

-- ----------------------
-- Create validation function for required metadata.workspace
-- ----------------------
CREATE OR REPLACE FUNCTION api.validate_metadata_workspace()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.metadata).workspace IS NULL OR (NEW.metadata).workspace = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10002","message": "metadata.workspace is required for the resource","hint": "Please provide a valid workspace in the metadata object"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    -- Validate workspace format (Kubernetes-style naming convention)
    IF NOT (NEW.metadata).workspace ~ '^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10005","message": "Invalid metadata.workspace format","hint": "Workspace must consist of lowercase alphanumeric characters, ''-'' or ''.'', must start and end with an alphanumeric character"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    -- Validate maximum length
    IF length((NEW.metadata).workspace) > 63 THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10006","message": "metadata.workspace is too long","hint": "Workspace cannot exceed 63 characters"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_workspace_on_clusters
    BEFORE INSERT OR UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();

CREATE TRIGGER validate_workspace_on_model_registries
    BEFORE INSERT OR UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();

CREATE TRIGGER validate_workspace_on_image_registries
    BEFORE INSERT OR UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();

CREATE TRIGGER validate_workspace_on_engines
    BEFORE INSERT OR UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();

CREATE TRIGGER validate_workspace_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();

CREATE TRIGGER validate_workspace_on_api_keys
    BEFORE INSERT OR UPDATE ON api.api_keys
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_workspace();
