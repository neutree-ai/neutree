-- NEU-627: ModelScope is a public registry too.

-- api.visibility enumerates the public kinds by name, so a provider added in Go
-- without a matching migration neither fails to build nor raises an error: the
-- registry reports itself private, and clients then offer it storage figures
-- that never arrive and write controls that cannot work. Redefined here rather
-- than edited into 084, which has shipped.
--
-- Must be kept in step with v1.VisibilityForModelRegistryType in
-- api/v1/model_registry_types.go, which is the same rule for callers inside the
-- control plane. Add every new public kind to both.
--
-- Body based on 084_model_registry_checked_at_and_visibility.up.sql, the latest
-- migration to define this function.
CREATE OR REPLACE FUNCTION api.visibility(registry api.model_registries)
RETURNS TEXT AS $$
    SELECT CASE
        WHEN (registry.spec).type IN ('hugging-face', 'model-scope') THEN 'public'
        ELSE 'private'
    END;
$$ LANGUAGE sql STABLE;

NOTIFY pgrst, 'reload schema';
