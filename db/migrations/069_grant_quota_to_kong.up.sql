-- Let the Kong gateway DB role evaluate per-API-key remaining quota
-- (api.get_api_key_remaining) directly, so the AI gateway plugin can enforce
-- quota on the request hot path. Conditional on the kong role existing: it is
-- created only in Kong-backed deployments, not in db-test / community-only
-- setups, so this migration is a no-op there.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kong_admin') THEN
        GRANT USAGE ON SCHEMA api TO kong_admin;
        GRANT EXECUTE ON FUNCTION api.get_api_key_remaining(uuid) TO kong_admin;
    END IF;
END $$;
