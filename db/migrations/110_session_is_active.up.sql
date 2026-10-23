-- NEU-656: let neutree-api reject the access token of an ended session.
--
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

BEGIN;

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
