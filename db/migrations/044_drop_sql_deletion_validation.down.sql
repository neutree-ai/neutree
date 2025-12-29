-- Restore SQL-level deletion validation
-- This is a rollback migration that restores the triggers and functions from 035

-- 1. WORKSPACE
CREATE OR REPLACE FUNCTION prevent_workspace_deletion_with_dependencies()
RETURNS TRIGGER AS $$
DECLARE
    endpoint_count INTEGER := 0;
    cluster_count INTEGER := 0;
    engine_count INTEGER := 0;
    model_registry_count INTEGER := 0;
    image_registry_count INTEGER := 0;
    model_catalog_count INTEGER := 0;
    role_count INTEGER := 0;
    api_key_count INTEGER := 0;
    role_assignment_count INTEGER := 0;
    error_msg TEXT;
BEGIN
    IF (NEW.metadata).deletion_timestamp IS NOT NULL
       AND (OLD.metadata).deletion_timestamp IS NULL THEN

        SELECT COUNT(*) INTO endpoint_count
        FROM api.endpoints
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO cluster_count
        FROM api.clusters
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO engine_count
        FROM api.engines
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO model_registry_count
        FROM api.model_registries
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO image_registry_count
        FROM api.image_registries
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO model_catalog_count
        FROM api.model_catalogs
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO role_count
        FROM api.roles
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO api_key_count
        FROM api.api_keys
        WHERE (metadata).workspace = (NEW.metadata).name;

        SELECT COUNT(*) INTO role_assignment_count
        FROM api.role_assignments
        WHERE (spec).workspace = (NEW.metadata).name;

        IF endpoint_count > 0 OR cluster_count > 0 OR engine_count > 0 OR
           model_registry_count > 0 OR image_registry_count > 0 OR model_catalog_count > 0 OR
           role_count > 0 OR api_key_count > 0 OR role_assignment_count > 0 THEN

            error_msg := 'Resources still exist in this workspace:';

            IF endpoint_count > 0 THEN
                error_msg := error_msg || format(E'\n- endpoints: %s', endpoint_count);
            END IF;

            IF cluster_count > 0 THEN
                error_msg := error_msg || format(E'\n- clusters: %s', cluster_count);
            END IF;

            IF engine_count > 0 THEN
                error_msg := error_msg || format(E'\n- engines: %s', engine_count);
            END IF;

            IF model_registry_count > 0 THEN
                error_msg := error_msg || format(E'\n- model_registries: %s', model_registry_count);
            END IF;

            IF image_registry_count > 0 THEN
                error_msg := error_msg || format(E'\n- image_registries: %s', image_registry_count);
            END IF;

            IF model_catalog_count > 0 THEN
                error_msg := error_msg || format(E'\n- model_catalogs: %s', model_catalog_count);
            END IF;

            IF role_count > 0 THEN
                error_msg := error_msg || format(E'\n- roles: %s', role_count);
            END IF;

            IF api_key_count > 0 THEN
                error_msg := error_msg || format(E'\n- api_keys: %s', api_key_count);
            END IF;

            IF role_assignment_count > 0 THEN
                error_msg := error_msg || format(E'\n- role_assignments: %s', role_assignment_count);
            END IF;

            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10125","message": "cannot delete workspace ''%s''","hint": "%s"}',
                    (NEW.metadata).name,
                    replace(error_msg, '"', '\"')),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_workspace_deletion
    BEFORE UPDATE ON api.workspaces
    FOR EACH ROW
    EXECUTE FUNCTION prevent_workspace_deletion_with_dependencies();

-- 2. CLUSTER
CREATE OR REPLACE FUNCTION prevent_cluster_deletion_with_endpoints()
RETURNS TRIGGER AS $$
DECLARE
    endpoint_count INTEGER;
BEGIN
    IF (NEW.metadata).deletion_timestamp IS NOT NULL
       AND (OLD.metadata).deletion_timestamp IS NULL THEN

        SELECT COUNT(*) INTO endpoint_count
        FROM api.endpoints
        WHERE (metadata).workspace = (NEW.metadata).workspace
          AND (spec).cluster = (NEW.metadata).name;

        IF endpoint_count > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10126","message": "cannot delete cluster ''%s/%s''","hint": "%s endpoint(s) still reference this cluster"}',
                    (NEW.metadata).workspace, (NEW.metadata).name, endpoint_count),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_cluster_deletion
    BEFORE UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION prevent_cluster_deletion_with_endpoints();

-- 3. IMAGE_REGISTRY
CREATE OR REPLACE FUNCTION prevent_image_registry_deletion_with_clusters()
RETURNS TRIGGER AS $$
DECLARE
    cluster_count INTEGER;
BEGIN
    IF (NEW.metadata).deletion_timestamp IS NOT NULL
       AND (OLD.metadata).deletion_timestamp IS NULL THEN

        SELECT COUNT(*) INTO cluster_count
        FROM api.clusters
        WHERE (metadata).workspace = (NEW.metadata).workspace
          AND (spec).image_registry = (NEW.metadata).name;

        IF cluster_count > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10127","message": "cannot delete image_registry ''%s/%s''","hint": "%s cluster(s) still reference this image registry"}',
                    (NEW.metadata).workspace, (NEW.metadata).name, cluster_count),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_image_registry_deletion
    BEFORE UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION prevent_image_registry_deletion_with_clusters();

-- 4. MODEL_REGISTRY
CREATE OR REPLACE FUNCTION prevent_model_registry_deletion_with_endpoints()
RETURNS TRIGGER AS $$
DECLARE
    endpoint_count INTEGER;
BEGIN
    IF (NEW.metadata).deletion_timestamp IS NOT NULL
       AND (OLD.metadata).deletion_timestamp IS NULL THEN

        SELECT COUNT(*) INTO endpoint_count
        FROM api.endpoints
        WHERE (metadata).workspace = (NEW.metadata).workspace
          AND ((spec).model).registry = (NEW.metadata).name;

        IF endpoint_count > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10128","message": "cannot delete model_registry ''%s/%s''","hint": "%s endpoint(s) still reference this model registry"}',
                    (NEW.metadata).workspace, (NEW.metadata).name, endpoint_count),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_model_registry_deletion
    BEFORE UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION prevent_model_registry_deletion_with_endpoints();

-- 5. ROLE
CREATE OR REPLACE FUNCTION prevent_role_deletion_with_assignments()
RETURNS TRIGGER AS $$
DECLARE
    assignment_count INTEGER;
BEGIN
    IF (NEW.metadata).deletion_timestamp IS NOT NULL
       AND (OLD.metadata).deletion_timestamp IS NULL THEN

        SELECT COUNT(*) INTO assignment_count
        FROM api.role_assignments
        WHERE ((metadata).workspace IS NOT DISTINCT FROM (NEW.metadata).workspace)
          AND (spec).role = (NEW.metadata).name;

        IF assignment_count > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10129","message": "cannot delete role ''%s/%s''","hint": "%s role assignment(s) still reference this role"}',
                    COALESCE((NEW.metadata).workspace, 'global'), (NEW.metadata).name, assignment_count),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_role_deletion
    BEFORE UPDATE ON api.roles
    FOR EACH ROW
    EXECUTE FUNCTION prevent_role_deletion_with_assignments();

-- 6. USER_PROFILE
CREATE OR REPLACE FUNCTION prevent_user_profile_deletion_with_assignments()
RETURNS TRIGGER AS $$
DECLARE
    assignment_count INTEGER;
BEGIN
    IF (NEW.metadata).deletion_timestamp IS NOT NULL
       AND (OLD.metadata).deletion_timestamp IS NULL THEN

        SELECT COUNT(*) INTO assignment_count
        FROM api.role_assignments
        WHERE (spec).user_id = NEW.id;

        IF assignment_count > 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10130","message": "cannot delete user_profile ''%s''","hint": "%s role assignment(s) still reference this user"}',
                    NEW.id, assignment_count),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER prevent_user_profile_deletion
    BEFORE UPDATE ON api.user_profiles
    FOR EACH ROW
    EXECUTE FUNCTION prevent_user_profile_deletion_with_assignments();
