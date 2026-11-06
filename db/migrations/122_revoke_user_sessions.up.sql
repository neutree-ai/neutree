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
-- without a session from older GoTrue versions. GoTrue indexes them by
-- (instance_id, user_id) and writes the nil UUID as instance_id, so the
-- delete names it to use that index. Access tokens already issued stay valid
-- until they expire (GOTRUE_JWT_EXP).
--
-- service_role only: it can end anyone's sessions.
CREATE OR REPLACE FUNCTION api.revoke_user_sessions(p_user_id UUID)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    DELETE FROM auth.refresh_tokens
    WHERE instance_id = '00000000-0000-0000-0000-000000000000'::uuid
      AND user_id = p_user_id::text;

    DELETE FROM auth.sessions WHERE user_id = p_user_id;
END;
$$;

REVOKE ALL ON FUNCTION api.revoke_user_sessions(UUID) FROM PUBLIC, anonymous, api_user;
GRANT EXECUTE ON FUNCTION api.revoke_user_sessions(UUID) TO service_role;
