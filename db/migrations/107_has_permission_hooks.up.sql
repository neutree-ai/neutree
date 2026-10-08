-- ----------------------
-- Split api.has_permission into an entry point and two extension hooks.
--
-- api.has_permission is the single permission check used by RLS policies,
-- SQL functions and the Go middleware. Its signature, parameter names
-- (part of the PostgREST RPC contract) and attributes stay unchanged; many
-- policies depend on it, so it is only ever replaced, never dropped.
--
-- The entry point now only orchestrates two hooks:
--   1. api.check_role_binding   -- does a role binding grant the permission?
--   2. api.check_api_key_scope  -- does the calling API key allow the request?
-- The key-scope hook runs only when the role-binding hook passed.
--
-- Extension contract:
--   * Community owns the entry point and the default hook bodies below.
--   * The enterprise edition overrides the hook bodies only (CREATE OR REPLACE
--     with the same signature and attributes); it does not redefine the entry.
--   * When a hook signature or the orchestration changes here, the enterprise
--     overrides must be updated in the same release.
--   * Further checks (for example an organization subtree check) are added
--     later as a separate function next to these hooks, not inside them.
--
-- Community behaviour is unchanged: only global role assignments count, the
-- workspace argument is ignored, and every request passes the key-scope hook.
-- ----------------------

-- Extension point: role binding.
-- Community: only global role assignments grant a permission; the workspace
-- argument is accepted but ignored. The enterprise edition overrides this
-- hook to also honour workspace-scoped role assignments. Keep the enterprise
-- override in sync when changing this function.
-- SECURITY DEFINER: reads api.role_assignments and api.roles, which are under
-- RLS that itself calls api.has_permission.
CREATE OR REPLACE FUNCTION api.check_role_binding(
    user_uuid UUID,
    required_permission api.permission_action,
    workspace TEXT DEFAULT NULL
)
RETURNS BOOLEAN AS $$
DECLARE
    has_perm BOOLEAN;
BEGIN
    SELECT EXISTS (
        SELECT 1
        FROM api.role_assignments ra
        JOIN api.roles r ON (ra.spec).role = (r.metadata).name
        WHERE (ra.spec).user_id = user_uuid
        AND (ra.spec).global = TRUE
        AND required_permission = ANY((r.spec).permissions)
    ) INTO has_perm;

    RETURN has_perm;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

COMMENT ON FUNCTION api.check_role_binding(UUID, api.permission_action, TEXT) IS
    'Extension point of api.has_permission (role binding). Community default; overridden by the enterprise edition.';

-- Extension point: API key scope.
-- Community: API keys carry no extra restriction, so this always passes. The
-- enterprise edition overrides this hook to restrict an API key request to
-- the workspace the key belongs to. Keep the enterprise override in sync when
-- changing this function.
-- SECURITY DEFINER: the enterprise override reads api.api_keys, which is
-- under RLS.
CREATE OR REPLACE FUNCTION api.check_api_key_scope(
    required_permission api.permission_action,
    workspace TEXT DEFAULT NULL
)
RETURNS BOOLEAN AS $$
BEGIN
    RETURN TRUE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

COMMENT ON FUNCTION api.check_api_key_scope(api.permission_action, TEXT) IS
    'Extension point of api.has_permission (API key scope). Community default; overridden by the enterprise edition.';

-- Entry point. Owned by community; the enterprise edition does not override it.
CREATE OR REPLACE FUNCTION api.has_permission(
    user_uuid UUID,
    required_permission api.permission_action,
    workspace TEXT DEFAULT NULL
)
RETURNS BOOLEAN AS $$
BEGIN
    -- The key-scope hook runs only when a role binding grants the permission.
    IF api.check_role_binding(user_uuid, required_permission, workspace) IS NOT TRUE THEN
        RETURN FALSE;
    END IF;

    RETURN api.check_api_key_scope(required_permission, workspace) IS TRUE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;
