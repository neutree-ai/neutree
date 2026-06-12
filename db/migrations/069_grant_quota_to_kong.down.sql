DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'kong_admin') THEN
        REVOKE EXECUTE ON FUNCTION api.get_api_key_remaining(uuid) FROM kong_admin;
    END IF;
END $$;
