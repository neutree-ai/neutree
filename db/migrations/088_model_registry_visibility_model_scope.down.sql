-- Restore the definition from 084: Hugging Face is the only public kind.
--
-- Rolling back leaves any stored model-scope registry reporting itself private,
-- which is the same state a build without the ModelScope provider would see.
CREATE OR REPLACE FUNCTION api.visibility(registry api.model_registries)
RETURNS TEXT AS $$
    SELECT CASE
        WHEN (registry.spec).type = 'hugging-face' THEN 'public'
        ELSE 'private'
    END;
$$ LANGUAGE sql STABLE;

NOTIFY pgrst, 'reload schema';
