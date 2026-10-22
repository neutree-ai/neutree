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

COMMIT;
