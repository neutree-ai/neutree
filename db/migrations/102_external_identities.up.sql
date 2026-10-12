-- NEU-656: link directory accounts to neutree users.
--
-- An LDAP login authenticates against the directory, not GoTrue, so neutree
-- needs its own record of which auth.users row a directory account owns. The
-- link is keyed by the directory's stable ID (entryUUID / objectGUID), never by
-- username or email: those can be renamed or reused, and matching on them would
-- let a new directory account take over an existing neutree user.
--
-- source names the directory the ID comes from ('ldap' while there is a single
-- source), so more sources can be added without migrating the key.
--
-- Only neutree-api reads and writes this table, through PostgREST with its
-- service_role token. The table sits in the api schema because that is the only
-- schema PostgREST serves, so access is closed off explicitly: the api schema's
-- default privileges grant every new table to api_user, which a user's session
-- token maps to, and that grant is revoked here. RLS is enabled with no policy
-- as a second lock; service_role has BYPASSRLS and is unaffected.
BEGIN;

CREATE TABLE api.external_identities (
    source      TEXT NOT NULL CHECK (source <> ''),
    external_id TEXT NOT NULL CHECK (external_id <> ''),
    user_id     UUID NOT NULL REFERENCES auth.users (id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT external_identities_source_external_id_key UNIQUE (source, external_id)
);

CREATE INDEX external_identities_user_id_idx ON api.external_identities (user_id);

REVOKE ALL ON api.external_identities FROM PUBLIC, api_user, anonymous, service_role;
GRANT SELECT, INSERT ON api.external_identities TO service_role;

ALTER TABLE api.external_identities ENABLE ROW LEVEL SECURITY;

-- Whether a GoTrue session still stands: it exists, has not passed its
-- not_after, and its user is not banned or deleted. A GoTrue access token is
-- valid until it expires, even after its session was ended (logout, or the
-- organization sync deactivating the user) or its user banned. neutree-api
-- asks this for the session_id claim of every user token it accepts, so such
-- a token stops working within its cache period instead of at its expiry.
--
-- service_role only: it reads auth.sessions and auth.users, which no client
-- may. plpgsql, so the body is not checked against the auth schema when the
-- migration runs.
CREATE OR REPLACE FUNCTION api.session_is_active(p_session_id UUID, p_user_id UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1
        FROM auth.sessions s
        JOIN auth.users u ON u.id = s.user_id
        WHERE s.id = p_session_id
          AND s.user_id = p_user_id
          AND (s.not_after IS NULL OR s.not_after > now())
          AND (u.banned_until IS NULL OR u.banned_until <= now())
          AND u.deleted_at IS NULL
    );
END;
$$;

REVOKE ALL ON FUNCTION api.session_is_active(UUID, UUID) FROM PUBLIC, anonymous, api_user;
GRANT EXECUTE ON FUNCTION api.session_is_active(UUID, UUID) TO service_role;

COMMIT;
