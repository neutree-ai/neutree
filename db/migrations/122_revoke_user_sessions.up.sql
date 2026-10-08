-- Ends every GoTrue session of a user: deactivating a user bans it and ends
-- its sessions.
--
-- A GoTrue ban refuses new sessions and refreshes, but leaves the sessions
-- and refresh tokens in place, so lifting the ban would bring them back.
-- GoTrue has no admin API that ends another user's sessions, so the
-- organization sync calls this function instead.
--
-- Deleting a user's auth.sessions rows also deletes their refresh tokens and
-- MFA claims (ON DELETE CASCADE on session_id). Refresh tokens are deleted by
-- user_id as well (kept as text in auth.refresh_tokens), which covers tokens
-- without a session from older GoTrue versions. Access tokens already issued
-- stay valid until they expire (GOTRUE_JWT_EXP).
--
-- Returns the number of sessions ended.
--
-- service_role only: it can end anyone's sessions.
CREATE OR REPLACE FUNCTION api.revoke_user_sessions(p_user_id UUID)
RETURNS INTEGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_sessions INTEGER;
BEGIN
    DELETE FROM auth.refresh_tokens WHERE user_id = p_user_id::text;

    DELETE FROM auth.sessions WHERE user_id = p_user_id;
    GET DIAGNOSTICS v_sessions = ROW_COUNT;

    RETURN v_sessions;
END;
$$;

REVOKE ALL ON FUNCTION api.revoke_user_sessions(UUID) FROM PUBLIC, anonymous, api_user;
GRANT EXECUTE ON FUNCTION api.revoke_user_sessions(UUID) TO service_role;
