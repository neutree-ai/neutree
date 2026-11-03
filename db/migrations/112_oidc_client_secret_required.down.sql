BEGIN;

DROP TRIGGER IF EXISTS identity_sources_require_client_secret ON api.identity_sources;
DROP FUNCTION IF EXISTS api.identity_sources_require_client_secret();

COMMIT;
