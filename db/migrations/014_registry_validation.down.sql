DROP TRIGGER IF EXISTS validate_model_registry_url_on_model_registry on api.model_registries;
DROP FUNCTION IF EXISTS api.validate_model_registry_url();

DROP TRIGGER IF EXISTS validate_image_registry_url_on_image_registry on api.image_registries;
DROP FUNCTION IF EXISTS api.validate_image_registry_url();

DROP TRIGGER IF EXISTS validate_image_registry_repo_on_image_registry on api.image_registries;
DROP FUNCTION IF EXISTS api.validate_image_registry_repo();