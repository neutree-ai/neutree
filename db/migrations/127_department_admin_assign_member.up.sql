-- ----------------------
-- Department administrators move local users within their subtree
-- ----------------------
-- Hand-written OrgUnit memberships (identity_source IS NULL):
--   * INSERT stays global-only: otherwise a subtree administrator could pull
--     an outsider (say, a global admin) into the subtree and then edit them
--     through user_profile:update.
--   * UPDATE needs org_unit:assign-member on the old and the new OrgUnit.
--   * DELETE needs it on the old OrgUnit.
-- api.has_permission_on_org_unit passes on a global assignment first, so
-- global assigners keep full access.

DROP POLICY IF EXISTS "org_unit_member update policy" ON api.org_unit_members;
DROP POLICY IF EXISTS "org_unit_member delete policy" ON api.org_unit_members;

CREATE POLICY "org_unit_member update policy" ON api.org_unit_members
    FOR UPDATE
    USING (
        identity_source IS NULL
        AND api.has_permission_on_org_unit(auth.uid(), 'org_unit:assign-member', org_unit_id)
    )
    WITH CHECK (
        identity_source IS NULL
        AND api.has_permission_on_org_unit(auth.uid(), 'org_unit:assign-member', org_unit_id)
    );

CREATE POLICY "org_unit_member delete policy" ON api.org_unit_members
    FOR DELETE
    USING (
        identity_source IS NULL
        AND api.has_permission_on_org_unit(auth.uid(), 'org_unit:assign-member', org_unit_id)
    );

-- Rewriting user_id is an INSERT in disguise (it brings another user into
-- the OrgUnit), and RLS cannot compare with the old row, so it keeps the
-- INSERT rule here.
CREATE OR REPLACE FUNCTION api.org_unit_members_keep_user()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.user_id IS DISTINCT FROM OLD.user_id
        AND auth.uid() IS NOT NULL
        AND api.has_permission(auth.uid(), 'org_unit:assign-member', NULL) IS NOT TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10287","message": "Only a global assigner can change the user of an OrgUnit membership","hint": "Remove the membership and have a global assigner add the other user"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION api.org_unit_members_keep_user() FROM PUBLIC;

CREATE TRIGGER org_unit_members_keep_user
    BEFORE UPDATE ON api.org_unit_members
    FOR EACH ROW
    EXECUTE FUNCTION api.org_unit_members_keep_user();

-- Preset role: department-admin gains org_unit:assign-member.
CREATE OR REPLACE FUNCTION api.update_department_admin_permissions()
RETURNS VOID AS $$
BEGIN
    UPDATE api.roles
    SET spec = ROW((spec).preset_key, ARRAY[
        'user_profile:read',
        'user_profile:update',
        'role_assignment:read',
        'role_assignment:create',
        'role_assignment:update',
        'role_assignment:delete',
        'role:read',
        'org_unit:read',
        'org_unit:assign-member'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'department-admin';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_department_admin_permissions();
