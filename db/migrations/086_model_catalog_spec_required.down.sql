-- Rollback NEU-654: drop the model catalog spec emptiness guard, restoring the
-- behaviour where a write may blank the whole composite spec column.
BEGIN;

DROP TRIGGER IF EXISTS validate_spec_on_model_catalogs ON api.model_catalogs;
DROP FUNCTION IF EXISTS api.validate_model_catalog_spec();
DROP FUNCTION IF EXISTS api.model_catalog_spec_is_empty(api.model_catalog_spec);

COMMIT;
