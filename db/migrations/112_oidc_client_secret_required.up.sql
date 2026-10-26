-- NEU-829: an OIDC identity source must have a client secret.
--
-- Only confidential clients are supported. The secret is write-only: a write
-- that leaves spec.oidc.client_secret empty keeps the stored one, so the check
-- runs on the stored secret, after api.identity_sources_before_write has
-- merged the new one in (BEFORE triggers fire in name order).
--
-- Status-only writes and soft deletes do not edit the configuration, so a
-- source stored without a secret can still report its status and be deleted.
BEGIN;

CREATE OR REPLACE FUNCTION api.identity_sources_require_client_secret()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF (NEW.spec).type IS DISTINCT FROM 'oidc'
       OR (NEW.metadata).deletion_timestamp IS NOT NULL
       OR (TG_OP = 'UPDATE' AND to_jsonb(NEW.spec) IS NOT DISTINCT FROM to_jsonb(OLD.spec)) THEN
        RETURN NEW;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM api.identity_source_secrets
         WHERE identity_source_id = NEW.id AND client_secret IS NOT NULL
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10255","message": "spec.oidc.client_secret is required","hint": "Provide the client secret the identity provider issued for spec.oidc.client_id"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION api.identity_sources_require_client_secret() FROM PUBLIC;

CREATE TRIGGER identity_sources_require_client_secret
    BEFORE INSERT OR UPDATE ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION api.identity_sources_require_client_secret();

COMMIT;
