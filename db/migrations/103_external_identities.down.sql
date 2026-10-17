BEGIN;

DROP FUNCTION IF EXISTS api.session_is_active(UUID, UUID);

DROP TABLE IF EXISTS api.external_identities;

COMMIT;
