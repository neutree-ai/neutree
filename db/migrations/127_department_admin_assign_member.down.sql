-- Reverts org_unit:assign-member for department-admin.

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
        'org_unit:read'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'department-admin';
END;
$$ LANGUAGE plpgsql;

SELECT api.update_department_admin_permissions();

DROP TRIGGER IF EXISTS org_unit_members_keep_user ON api.org_unit_members;
DROP FUNCTION IF EXISTS api.org_unit_members_keep_user();

-- Policies as in the org_units_and_teams migration.
DROP POLICY IF EXISTS "org_unit_member update policy" ON api.org_unit_members;
DROP POLICY IF EXISTS "org_unit_member delete policy" ON api.org_unit_members;

CREATE POLICY "org_unit_member update policy" ON api.org_unit_members
    FOR UPDATE
    USING (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    )
    WITH CHECK (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    );

CREATE POLICY "org_unit_member delete policy" ON api.org_unit_members
    FOR DELETE
    USING (
        identity_source IS NULL
        AND (SELECT api.has_permission(auth.uid(), 'org_unit:assign-member', NULL))
    );
