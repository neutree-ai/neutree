-- Restore the single-function api.has_permission from 001_rbac.up.sql and
-- remove the extension hooks. The entry point is replaced first so that it no
-- longer calls the hooks when they are dropped.
CREATE OR REPLACE FUNCTION api.has_permission(
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

DROP FUNCTION IF EXISTS api.check_api_key_scope(api.permission_action, TEXT);
DROP FUNCTION IF EXISTS api.check_role_binding(UUID, api.permission_action, TEXT);
