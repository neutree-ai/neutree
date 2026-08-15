-- NEU-654: reject a model catalog whose spec declares neither a model nor
-- variants.
--
-- A PATCH replaces the whole composite spec column instead of merging into it,
-- so a request carrying an empty spec (`spec: {}`, `spec: null`, or an object
-- whose only keys the Go DTO does not know) does not partially update the
-- catalog -- it blanks every attribute. The API middleware rejects those
-- payloads, but it is not the only writer: neutree-core and the CLI reach
-- PostgREST directly with the service role and bypass it entirely. The
-- invariant belongs here, next to the metadata required-field triggers in
-- 004_required_fields.
--
-- An ordinary catalog declares spec.model; a recipe catalog declares
-- spec.variants. Note the row-wise IS NULL semantics: for a composite value it
-- is true when every attribute is null, which is exactly the blanked spec this
-- guards against.
BEGIN;

CREATE OR REPLACE FUNCTION api.model_catalog_spec_is_empty(spec api.model_catalog_spec)
RETURNS BOOLEAN AS $$
    SELECT spec IS NULL
        OR (
            (spec).model IS NULL
            AND (
                (spec).variants IS NULL
                OR (spec).variants::jsonb = '{}'::jsonb
            )
        );
$$ LANGUAGE sql IMMUTABLE;

CREATE OR REPLACE FUNCTION api.validate_model_catalog_spec()
RETURNS TRIGGER AS $$
BEGIN
    IF NOT api.model_catalog_spec_is_empty(NEW.spec) THEN
        RETURN NEW;
    END IF;

    -- Only the non-empty -> empty transition is destructive. A catalog whose
    -- spec is already empty predates this guard and stays writable: blocking it
    -- would stop its controller from writing status, leaving the row unable to
    -- reconcile and unable to be repaired.
    IF TG_OP = 'UPDATE' AND api.model_catalog_spec_is_empty(OLD.spec) THEN
        RETURN NEW;
    END IF;

    RAISE sqlstate 'PGRST'
        USING message = '{"code": "10045","message": "model_catalog spec must declare a model or variants","hint": "Omit spec to leave it unchanged, or send a spec with a model (variants for a recipe catalog)"}',
              detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER validate_spec_on_model_catalogs
    BEFORE INSERT OR UPDATE ON api.model_catalogs
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_model_catalog_spec();

COMMIT;
