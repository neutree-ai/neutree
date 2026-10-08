-- Take identity_source:* out of every role; the enum values themselves stay
-- (see 104's down migration).
UPDATE api.roles
SET spec = ROW(
    (spec).preset_key,
    ARRAY(
        SELECT p
        FROM unnest((spec).permissions) AS p
        WHERE p::text NOT LIKE 'identity_source:%'
    )::api.permission_action[]
)::api.role_spec
WHERE EXISTS (
    SELECT 1 FROM unnest((spec).permissions) AS p WHERE p::text LIKE 'identity_source:%'
);

DROP FUNCTION IF EXISTS api.get_identity_source_secrets(TEXT);
DROP FUNCTION IF EXISTS api.list_login_identity_sources();

DROP TABLE IF EXISTS api.identity_sources;
DROP TABLE IF EXISTS api.identity_source_secrets;

DROP FUNCTION IF EXISTS api.identity_sources_after_delete();
DROP FUNCTION IF EXISTS api.identity_sources_before_write();

DROP TYPE IF EXISTS api.identity_source_status;
DROP TYPE IF EXISTS api.identity_source_connection_test;
DROP TYPE IF EXISTS api.identity_source_spec;
DROP TYPE IF EXISTS api.identity_source_oidc_spec;
DROP TYPE IF EXISTS api.identity_source_oidc_claims;
DROP TYPE IF EXISTS api.identity_source_ldap_spec;
DROP TYPE IF EXISTS api.identity_source_ldap_attributes;
