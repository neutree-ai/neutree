-- ----------------------
-- Resource: IdentitySource (NEU-656)
-- ----------------------
-- An LDAP directory or OpenID Connect provider users can log in with. Global:
-- metadata.workspace is always NULL, and access is by the identity_source:*
-- permissions (migration 104) with a NULL workspace.
--
-- metadata.name is the immutable id of the source. It becomes part of the
-- users' link source (ldap:<name> / oidc:<name> in api.external_identities)
-- and of their placeholder email domain (<name>.<type>.neutree.local), so it
-- is a DNS label of at most 32 characters and cannot be renamed.
--
-- Secrets (spec.ldap.bind_password, spec.oidc.client_secret) are write-only.
-- A write carries them in spec; the trigger below encrypts them into
-- api.identity_source_secrets and stores spec with the attribute set to NULL,
-- so no read of api.identity_sources ever returns one. A write that leaves the
-- attribute empty keeps the stored secret, which is what makes a partial spec
-- update (PostgREST replaces the whole composite) safe for them.
--
-- Encryption is pgp_sym_encrypt (OpenPGP, AES-256, random salt and session
-- key per message, so equal secrets give different ciphertexts) keyed by
-- app.settings.jwt_secret, the key the API key encryption (012) uses.
-- PostgREST sets it on every request (PGRST_APP_SETTINGS_JWT_SECRET).

CREATE TYPE api.identity_source_ldap_attributes AS (
    id TEXT,
    username TEXT,
    email TEXT,
    display_name TEXT,
    member_of TEXT
);

CREATE TYPE api.identity_source_ldap_spec AS (
    url TEXT,
    start_tls BOOLEAN,
    ca_cert TEXT,
    insecure_skip_verify BOOLEAN,
    timeout INTEGER, -- seconds
    bind_dn TEXT,
    bind_password TEXT, -- write-only, always stored as NULL
    user_base_dn TEXT,
    user_filter TEXT,
    attributes api.identity_source_ldap_attributes
);

CREATE TYPE api.identity_source_oidc_claims AS (
    username TEXT,
    display_name TEXT,
    email TEXT
);

CREATE TYPE api.identity_source_oidc_spec AS (
    issuer TEXT,
    client_id TEXT,
    client_secret TEXT, -- write-only, always stored as NULL
    scopes TEXT[],
    ca_cert TEXT,
    redirect_url TEXT,
    claims api.identity_source_oidc_claims,
    allowed_redirects TEXT[]
);

CREATE TYPE api.identity_source_spec AS (
    type TEXT,
    enabled BOOLEAN,
    ldap api.identity_source_ldap_spec,
    oidc api.identity_source_oidc_spec
);

CREATE TYPE api.identity_source_connection_test AS (
    time TIMESTAMPTZ,
    ok BOOLEAN,
    message TEXT
);

CREATE TYPE api.identity_source_status AS (
    phase TEXT,
    last_transition_time TIMESTAMPTZ,
    error_message TEXT,
    last_connection_test api.identity_source_connection_test
);

CREATE TABLE api.identity_sources (
    id SERIAL PRIMARY KEY,
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    metadata api.metadata,
    spec api.identity_source_spec,
    status api.identity_source_status
);

CREATE UNIQUE INDEX identity_sources_name_unique_idx ON api.identity_sources (((metadata).name));

-- The encrypted secrets, one row per identity source. Nobody but the trigger
-- and api.get_identity_source_secrets (both SECURITY DEFINER) touches it: the
-- api schema's default privileges grant new tables to api_user and
-- service_role, and that is revoked here. RLS without a policy is a second
-- lock. There is no foreign key because the BEFORE INSERT trigger writes the
-- row before the identity source row exists; the AFTER DELETE trigger removes
-- it again.
CREATE TABLE api.identity_source_secrets (
    identity_source_id INTEGER PRIMARY KEY,
    bind_password BYTEA,
    client_secret BYTEA
);

REVOKE ALL ON api.identity_source_secrets FROM PUBLIC, api_user, anonymous, service_role;

ALTER TABLE api.identity_source_secrets ENABLE ROW LEVEL SECURITY;

CREATE OR REPLACE FUNCTION api.identity_sources_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_meta api.metadata := NEW.metadata;
    v_spec api.identity_source_spec := NEW.spec;
    v_ldap api.identity_source_ldap_spec;
    v_oidc api.identity_source_oidc_spec;
    v_attrs api.identity_source_ldap_attributes;
    v_claims api.identity_source_oidc_claims;
    v_bind_password TEXT;
    v_client_secret TEXT;
    v_key TEXT;
    v_has_bind_password BOOLEAN;
    v_missing TEXT;
    v_redirect TEXT;
BEGIN
    IF v_meta.name IS NULL OR v_meta.name !~ '^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10250","message": "Invalid metadata.name for an identity source","hint": "Use 1-32 lowercase letters, digits or inner ''-''; the name becomes part of user links and placeholder email domains"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF TG_OP = 'UPDATE' AND ((OLD.metadata).name IS DISTINCT FROM v_meta.name OR OLD.id IS DISTINCT FROM NEW.id) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10251","message": "metadata.name of an identity source cannot be changed","hint": "Users are linked to the source by its name; create a new identity source instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF COALESCE(v_meta.workspace, '') <> '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10252","message": "An identity source is global and has no workspace","hint": "Remove metadata.workspace"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_meta.workspace := NULL;

    IF COALESCE(btrim(v_meta.display_name), '') = '' THEN
        v_meta.display_name := v_meta.name;
    END IF;

    NEW.metadata := v_meta;

    -- A write that does not touch spec (a status update, a soft delete) has
    -- nothing more to check, and the stored spec holds no secret.
    IF TG_OP = 'UPDATE' AND to_jsonb(NEW.spec) IS NOT DISTINCT FROM to_jsonb(OLD.spec) THEN
        RETURN NEW;
    END IF;

    IF v_spec IS NULL OR v_spec.type IS NULL OR v_spec.type NOT IN ('ldap', 'oidc') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10253","message": "spec.type must be ldap or oidc","hint": "Set spec.type and the matching spec.ldap or spec.oidc"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.enabled := COALESCE(v_spec.enabled, FALSE);

    -- A sub-object whose attributes are all NULL counts as absent ("IS NULL"
    -- is true for it): the resource proxy's secret backfill can produce one
    -- for the sub-object of the previous type.
    IF v_spec.type = 'ldap' THEN
        IF NOT (v_spec.oidc IS NULL) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10254","message": "spec.oidc must be empty when spec.type is ldap","hint": "Remove spec.oidc"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF v_spec.ldap IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10254","message": "spec.ldap is required when spec.type is ldap","hint": "Provide spec.ldap"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_ldap := v_spec.ldap;

        v_missing := CASE
            WHEN COALESCE(btrim(v_ldap.url), '') = '' THEN 'spec.ldap.url'
            WHEN COALESCE(btrim(v_ldap.bind_dn), '') = '' THEN 'spec.ldap.bind_dn'
            WHEN COALESCE(btrim(v_ldap.user_base_dn), '') = '' THEN 'spec.ldap.user_base_dn'
            WHEN COALESCE(btrim(v_ldap.user_filter), '') = '' THEN 'spec.ldap.user_filter'
        END;

        IF v_missing IS NOT NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10255","message": "%s is required","hint": "Provide %s"}', v_missing, v_missing),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF v_ldap.url !~* '^ldaps?://[^/?#\s]+' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10256","message": "spec.ldap.url must be an ldap:// or ldaps:// URL","hint": "Use ldap://host:389 or ldaps://host:636"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF COALESCE(v_ldap.start_tls, FALSE) AND v_ldap.url ~* '^ldaps://' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10258","message": "spec.ldap.start_tls cannot be used with an ldaps:// url","hint": "Use ldap:// with start_tls, or ldaps:// without it"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF COALESCE(v_ldap.timeout, 0) < 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10258","message": "spec.ldap.timeout must not be negative","hint": "Set a timeout in seconds, or leave it empty for the default"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF position('{username}' IN v_ldap.user_filter) = 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10257","message": "spec.ldap.user_filter must contain {username}","hint": "For example (&(objectClass=inetOrgPerson)(uid={username}))"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF COALESCE(btrim(v_ldap.ca_cert), '') <> '' AND position('-----BEGIN CERTIFICATE-----' IN v_ldap.ca_cert) = 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10258","message": "spec.ldap.ca_cert must hold PEM certificates","hint": "Paste the CA certificate(s) in PEM form"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_ldap.start_tls := COALESCE(v_ldap.start_tls, FALSE);
        v_ldap.insecure_skip_verify := COALESCE(v_ldap.insecure_skip_verify, FALSE);
        v_ldap.timeout := COALESCE(NULLIF(v_ldap.timeout, 0), 10);
        v_ldap.ca_cert := NULLIF(btrim(v_ldap.ca_cert), '');

        v_attrs := v_ldap.attributes;
        v_attrs.id := COALESCE(NULLIF(btrim(v_attrs.id), ''), 'entryUUID');
        v_attrs.username := COALESCE(NULLIF(btrim(v_attrs.username), ''), 'uid');
        v_attrs.email := COALESCE(NULLIF(btrim(v_attrs.email), ''), 'mail');
        v_attrs.display_name := COALESCE(NULLIF(btrim(v_attrs.display_name), ''), 'cn');
        v_attrs.member_of := NULLIF(btrim(v_attrs.member_of), '');
        v_ldap.attributes := v_attrs;

        v_bind_password := NULLIF(v_ldap.bind_password, '');
        v_ldap.bind_password := NULL;

        v_spec.ldap := v_ldap;
        v_spec.oidc := NULL;
    ELSE
        IF NOT (v_spec.ldap IS NULL) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10254","message": "spec.ldap must be empty when spec.type is oidc","hint": "Remove spec.ldap"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF v_spec.oidc IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10254","message": "spec.oidc is required when spec.type is oidc","hint": "Provide spec.oidc"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_oidc := v_spec.oidc;

        v_missing := CASE
            WHEN COALESCE(btrim(v_oidc.issuer), '') = '' THEN 'spec.oidc.issuer'
            WHEN COALESCE(btrim(v_oidc.client_id), '') = '' THEN 'spec.oidc.client_id'
            WHEN COALESCE(btrim(v_oidc.redirect_url), '') = '' THEN 'spec.oidc.redirect_url'
            WHEN COALESCE(cardinality(v_oidc.allowed_redirects), 0) = 0 THEN 'spec.oidc.allowed_redirects'
        END;

        IF v_missing IS NOT NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10255","message": "%s is required","hint": "Provide %s"}', v_missing, v_missing),
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF v_oidc.issuer !~* '^https?://[^/?#\s]+' OR v_oidc.redirect_url !~* '^https?://[^/?#\s]+' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10256","message": "spec.oidc.issuer and spec.oidc.redirect_url must be http(s) URLs","hint": "For example https://idp.example.org/realms/neutree"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        FOREACH v_redirect IN ARRAY v_oidc.allowed_redirects LOOP
            IF v_redirect IS NULL OR v_redirect !~* '^https?://[^/?#@\s]+(/[^?#\s]*)?$' THEN
                RAISE sqlstate 'PGRST'
                    USING message = '{"code": "10256","message": "spec.oidc.allowed_redirects must be http(s) URLs without user, query or fragment","hint": "For example https://neutree.example.org/"}',
                    detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
            END IF;
        END LOOP;

        IF COALESCE(cardinality(v_oidc.scopes), 0) = 0 THEN
            v_oidc.scopes := ARRAY['openid', 'profile', 'email'];
        ELSIF NOT ('openid' = ANY (v_oidc.scopes)) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10257","message": "spec.oidc.scopes must include openid","hint": "Add openid to spec.oidc.scopes, or leave it empty for openid, profile, email"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF COALESCE(btrim(v_oidc.ca_cert), '') <> '' AND position('-----BEGIN CERTIFICATE-----' IN v_oidc.ca_cert) = 0 THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10258","message": "spec.oidc.ca_cert must hold PEM certificates","hint": "Paste the CA certificate(s) in PEM form"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_oidc.ca_cert := NULLIF(btrim(v_oidc.ca_cert), '');

        v_claims := v_oidc.claims;
        v_claims.username := COALESCE(NULLIF(btrim(v_claims.username), ''), 'preferred_username');
        v_claims.display_name := COALESCE(NULLIF(btrim(v_claims.display_name), ''), 'name');
        v_claims.email := COALESCE(NULLIF(btrim(v_claims.email), ''), 'email');
        v_oidc.claims := v_claims;

        v_client_secret := NULLIF(v_oidc.client_secret, '');
        v_oidc.client_secret := NULL;

        v_spec.oidc := v_oidc;
        v_spec.ldap := NULL;
    END IF;

    NEW.spec := v_spec;

    IF v_bind_password IS NOT NULL OR v_client_secret IS NOT NULL THEN
        v_key := current_setting('app.settings.jwt_secret', true);

        IF COALESCE(v_key, '') = '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10259","message": "Identity source secrets cannot be encrypted","hint": "app.settings.jwt_secret is not set for this database session"}',
                detail = '{"status": 500, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END IF;

    -- A secret is replaced when a new one is given, kept when it is left
    -- empty, and dropped when the source changes to the other type.
    INSERT INTO api.identity_source_secrets (identity_source_id)
    VALUES (NEW.id)
    ON CONFLICT (identity_source_id) DO NOTHING;

    UPDATE api.identity_source_secrets s
    SET bind_password = CASE
            WHEN v_spec.type <> 'ldap' THEN NULL
            WHEN v_bind_password IS NOT NULL THEN pgp_sym_encrypt(v_bind_password, v_key, 'cipher-algo=aes256')
            ELSE s.bind_password
        END,
        client_secret = CASE
            WHEN v_spec.type <> 'oidc' THEN NULL
            WHEN v_client_secret IS NOT NULL THEN pgp_sym_encrypt(v_client_secret, v_key, 'cipher-algo=aes256')
            ELSE s.client_secret
        END
    WHERE s.identity_source_id = NEW.id
    RETURNING s.bind_password IS NOT NULL INTO v_has_bind_password;

    IF v_spec.type = 'ldap' AND NOT v_has_bind_password THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10255","message": "spec.ldap.bind_password is required","hint": "Provide the password of the service account in spec.ldap.bind_dn"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION api.identity_sources_after_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    DELETE FROM api.identity_source_secrets WHERE identity_source_id = OLD.id;
    RETURN OLD;
END;
$$;

CREATE TRIGGER update_identity_sources_update_timestamp
    BEFORE UPDATE ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_identity_sources_default_timestamp
    BEFORE INSERT ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER enforce_soft_delete_integrity_identity_sources
    BEFORE UPDATE ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_soft_delete();

CREATE TRIGGER identity_sources_before_write
    BEFORE INSERT OR UPDATE ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION api.identity_sources_before_write();

CREATE TRIGGER identity_sources_after_delete
    AFTER DELETE ON api.identity_sources
    FOR EACH ROW
    EXECUTE FUNCTION api.identity_sources_after_delete();

ALTER TABLE api.identity_sources ENABLE ROW LEVEL SECURITY;

CREATE POLICY "identity_source read policy" ON api.identity_sources
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'identity_source:read', NULL)
    );

CREATE POLICY "identity_source create policy" ON api.identity_sources
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'identity_source:create', NULL)
    );

-- Soft delete (setting deletion_timestamp) needs identity_source:delete, as
-- for every other resource (022).
CREATE POLICY "identity_source update policy" ON api.identity_sources
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'identity_source:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'identity_source:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
        )
    );

CREATE POLICY "identity_source delete policy" ON api.identity_sources
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'identity_source:delete', NULL)
    );

-- The login page lists the enabled sources before anyone is logged in, so this
-- is open to the anonymous role. It returns only what the page shows.
CREATE OR REPLACE FUNCTION api.list_login_identity_sources()
RETURNS TABLE (name TEXT, display_name TEXT, type TEXT)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT (i.metadata).name,
           COALESCE(NULLIF((i.metadata).display_name, ''), (i.metadata).name),
           (i.spec).type
    FROM api.identity_sources i
    WHERE (i.spec).enabled IS TRUE
      AND (i.metadata).deletion_timestamp IS NULL
    ORDER BY (i.metadata).name;
$$;

REVOKE ALL ON FUNCTION api.list_login_identity_sources() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION api.list_login_identity_sources() TO anonymous, api_user, service_role;

-- Decrypts the secrets of a (not deleted) identity source for neutree-api's
-- login code. Only service_role may call it; NULL means no secret is stored.
CREATE OR REPLACE FUNCTION api.get_identity_source_secrets(p_name TEXT)
RETURNS TABLE (ldap_bind_password TEXT, oidc_client_secret TEXT)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE
    v_key TEXT := current_setting('app.settings.jwt_secret', true);
BEGIN
    IF COALESCE(v_key, '') = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10259","message": "Identity source secrets cannot be decrypted","hint": "app.settings.jwt_secret is not set for this database session"}',
            detail = '{"status": 500, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN QUERY
    SELECT pgp_sym_decrypt(s.bind_password, v_key),
           pgp_sym_decrypt(s.client_secret, v_key)
    FROM api.identity_sources i
    JOIN api.identity_source_secrets s ON s.identity_source_id = i.id
    WHERE (i.metadata).name = p_name
      AND (i.metadata).deletion_timestamp IS NULL;
END;
$$;

REVOKE ALL ON FUNCTION api.get_identity_source_secrets(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION api.get_identity_source_secrets(TEXT) TO service_role;

-- Trigger functions are not meant to be called; keep them off the RPC surface.
REVOKE ALL ON FUNCTION api.identity_sources_before_write() FROM PUBLIC;
REVOKE ALL ON FUNCTION api.identity_sources_after_delete() FROM PUBLIC;

-- Admin holds every permission in the enum, including identity_source:*.
-- Workspace roles get nothing: identity sources are platform configuration.
SELECT api.update_admin_permissions();
