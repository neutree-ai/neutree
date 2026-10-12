DROP FUNCTION IF EXISTS api.list_identity_source_sync_users(TEXT, TEXT);

REVOKE UPDATE (username, email, sync_deactivated) ON api.external_identities FROM service_role;

ALTER TABLE api.external_identities
    DROP COLUMN IF EXISTS sync_deactivated,
    DROP COLUMN IF EXISTS email,
    DROP COLUMN IF EXISTS username;

DROP FUNCTION IF EXISTS api.request_identity_source_sync(TEXT);

DROP TRIGGER IF EXISTS identity_sources_sync_before_write ON api.identity_sources;
DROP FUNCTION IF EXISTS api.identity_sources_sync_before_write();

ALTER TYPE api.identity_source_status DROP ATTRIBUTE IF EXISTS last_sync;
ALTER TYPE api.identity_source_spec DROP ATTRIBUTE IF EXISTS sync;
ALTER TYPE api.identity_source_ldap_spec DROP ATTRIBUTE IF EXISTS sync;

DROP TYPE IF EXISTS api.identity_source_sync_status;
DROP TYPE IF EXISTS api.identity_source_ldap_sync_spec;
DROP TYPE IF EXISTS api.identity_source_sync_spec;
