------------------------------------
-- Image Registry Repo Validation
------------------------------------
CREATE OR REPLACE FUNCTION api.validate_image_registry_repo()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).repository IS NULL OR trim((NEW.spec).repository) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10013","message": "spec.repository is required","hint": "Provide Image Registry Repo"}',
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