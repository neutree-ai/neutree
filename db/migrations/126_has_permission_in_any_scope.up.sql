-- ----------------------
-- api.has_permission_in_any_scope
-- ----------------------
-- Does user_uuid hold required_permission on any scope: a global role
-- assignment, or an OrgUnit-scoped one that api.check_org_role_binding
-- accepts with no target OrgUnit? The UI uses it to decide which entries to
-- show. It authorizes nothing: RLS checks every target on its own.
CREATE OR REPLACE FUNCTION api.has_permission_in_any_scope(
    user_uuid UUID,
    required_permission api.permission_action
)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
AS $$
    SELECT api.has_permission_on_org_unit(user_uuid, required_permission, NULL) IS TRUE;
$$;
