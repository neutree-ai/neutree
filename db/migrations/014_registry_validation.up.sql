------------------------------------
-- Model Registry URL Validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_model_registry_url()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).type = 'bentoml' AND ((NEW.spec).url IS NULL OR (NEW.spec).url = '') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10008","message": "spec.url is required","hint": "Provide file system path"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF (NEW.spec).type = 'hugging-face' AND ((NEW.spec).url IS NULL OR (NEW.spec).url = '') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10008","message": "spec.url is required","hint": "Provide Hugging Face URL"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_model_registry_url_on_model_registry
    BEFORE INSERT OR UPDATE ON api.model_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_model_registry_url();


------------------------------------
-- Image Registry URL Validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_image_registry_url()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).url IS NULL OR (NEW.spec).url = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10009","message": "spec.url is required","hint": "Provide Image Registry URL"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_image_registry_url_on_image_registry
    BEFORE INSERT OR UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_image_registry_url();

------------------------------------
-- Image Registry Repo Validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_image_registry_repo()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).repository IS NULL OR (NEW.spec).repository = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10010","message": "spec.repository is required","hint": "Provide Image Registry Repo"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS validate_image_registry_repo_on_image_registry ON api.image_registries;
CREATE TRIGGER validate_image_registry_repo_on_image_registry
    BEFORE INSERT OR UPDATE ON api.image_registries
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_image_registry_repo();