-- api.has_permission becomes an entry point that calls two hooks. The
-- enterprise edition replaces only the hook bodies (same signature and
-- attributes), so a hook signature change here must ship with its overrides.
-- The entry point keeps its signature and parameter names (PostgREST RPC
-- contract) and is never dropped, because RLS policies depend on it.

-- Default: only global role assignments count; workspace is ignored.
-- SECURITY DEFINER: the tables it reads are under RLS that calls has_permission.
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

-- Default: API keys add no restriction.
-- SECURITY DEFINER: the enterprise override reads api.api_keys, which is under RLS.
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

CREATE OR REPLACE FUNCTION api.has_permission(
    user_uuid UUID,
    required_permission api.permission_action,
    workspace TEXT DEFAULT NULL
)
RETURNS BOOLEAN AS $$
BEGIN
    IF api.check_role_binding(user_uuid, required_permission, workspace) IS NOT TRUE THEN
        RETURN FALSE;
    END IF;

    RETURN api.check_api_key_scope(required_permission, workspace) IS TRUE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;
