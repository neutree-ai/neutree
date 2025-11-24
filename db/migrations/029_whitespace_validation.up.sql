------------------------------------
-- Add Leading/Trailing Whitespace Validation
------------------------------------
-- Reject fields with leading or trailing whitespace

------------------------------------
-- Image Registry: Whitespace validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_image_registry_no_whitespace()
RETURNS TRIGGER AS $$
BEGIN
    -- Check URL for leading/trailing whitespace
    IF (NEW.spec).url IS NOT NULL
       AND trim((NEW.spec).url) != ''
       AND (NEW.spec).url != trim((NEW.spec).url) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10116","message": "spec.url has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the URL"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Check repository for leading/trailing whitespace
    IF (NEW.spec).repository IS NOT NULL
       AND trim((NEW.spec).repository) != ''
       AND (NEW.spec).repository != trim((NEW.spec).repository) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10117","message": "spec.repository has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the repository"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Check name for leading/trailing whitespace
    IF (NEW.metadata).name IS NOT NULL
       AND trim((NEW.metadata).name) != ''
       AND (NEW.metadata).name != trim((NEW.metadata).name) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10118","message": "metadata.name has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_image_registry_no_whitespace_trigger
    BEFORE INSERT OR UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_image_registry_no_whitespace();

------------------------------------
-- Model Registry: Whitespace validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_model_registry_no_whitespace()
RETURNS TRIGGER AS $$
BEGIN
    -- Check URL for leading/trailing whitespace
    IF (NEW.spec).url IS NOT NULL
       AND trim((NEW.spec).url) != ''
       AND (NEW.spec).url != trim((NEW.spec).url) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10119","message": "spec.url has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the URL"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Check name for leading/trailing whitespace
    IF (NEW.metadata).name IS NOT NULL
       AND trim((NEW.metadata).name) != ''
       AND (NEW.metadata).name != trim((NEW.metadata).name) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10120","message": "metadata.name has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_model_registry_no_whitespace_trigger
    BEFORE INSERT OR UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_model_registry_no_whitespace();

------------------------------------
-- Endpoint: Whitespace validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_endpoint_no_whitespace()
RETURNS TRIGGER AS $$
BEGIN
    -- Check name for leading/trailing whitespace
    IF (NEW.metadata).name IS NOT NULL
       AND trim((NEW.metadata).name) != ''
       AND (NEW.metadata).name != trim((NEW.metadata).name) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10121","message": "metadata.name has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_endpoint_no_whitespace_trigger
    BEFORE INSERT OR UPDATE ON api.endpoints
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_endpoint_no_whitespace();

------------------------------------
-- Cluster: Whitespace validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_cluster_no_whitespace()
RETURNS TRIGGER AS $$
BEGIN
    -- Check name for leading/trailing whitespace
    IF (NEW.metadata).name IS NOT NULL
       AND trim((NEW.metadata).name) != ''
       AND (NEW.metadata).name != trim((NEW.metadata).name) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10123","message": "metadata.name has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_cluster_no_whitespace_trigger
    BEFORE INSERT OR UPDATE ON api.clusters
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_cluster_no_whitespace();

------------------------------------
-- Engine: Whitespace validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_engine_no_whitespace()
RETURNS TRIGGER AS $$
BEGIN
    -- Check name for leading/trailing whitespace
    IF (NEW.metadata).name IS NOT NULL
       AND trim((NEW.metadata).name) != ''
       AND (NEW.metadata).name != trim((NEW.metadata).name) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10124","message": "metadata.name has leading or trailing whitespace","hint": "Remove spaces from the beginning or end of the name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_engine_no_whitespace_trigger
    BEFORE INSERT OR UPDATE ON api.engines
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_engine_no_whitespace();
