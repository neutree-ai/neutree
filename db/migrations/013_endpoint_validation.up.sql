CREATE OR REPLACE FUNCTION api.validate_endpoint_model_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.name IS NULL OR trim((NEW.spec).model.name) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10007","message": "spec.model.name is required","hint": "Provide model name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF NOT (NEW.spec).model.name ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10105","message": "Invalid model name format","hint": "Use lowercase alphanumeric and hyphens"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_name_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model_name();


CREATE OR REPLACE FUNCTION api.validate_endpoint_model_version()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.version IS NULL OR trim((NEW.spec).model.version) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10008","message": "spec.model.version is required","hint": "Provide model version"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_version_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model_version();


CREATE OR REPLACE FUNCTION api.validate_endpoint_model_file()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.file IS NULL OR trim((NEW.spec).model.file) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10009","message": "spec.model.file is required","hint": "Provide model file"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_file_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model_file();


CREATE OR REPLACE FUNCTION api.validate_endpoint_replica_count()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).replicas.num IS NULL OR (NEW.spec).replicas.num < 1 THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10106","message": "spec.replicas.num must be at least 1","hint": "Provide a valid replica count"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_replica_count_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_replica_count();

CREATE OR REPLACE FUNCTION api.validate_endpoint_cluster_name()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).cluster IS NULL OR trim((NEW.spec).cluster) = ''
    THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10010","message": "spec.cluster is required","hint": "Provide cluster name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_cluster_name_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_cluster_name();

CREATE OR REPLACE FUNCTION api.validate_endpoint_model_registry()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).model.registry IS NULL OR trim((NEW.spec).model.registry) = ''
    THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10011","message": "spec.model.registry is required","hint": "Provide model registry"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_model_registry_on_endpoints
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_model_registry();
