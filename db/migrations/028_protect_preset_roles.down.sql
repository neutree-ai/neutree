DROP POLICY IF EXISTS "role update policy" ON api.roles;
DROP POLICY IF EXISTS "role delete policy" ON api.roles;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment delete policy" ON api.role_assignments;

CREATE POLICY "role update policy" ON api.roles
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'role:update', NULL)
    );

CREATE POLICY "role delete policy" ON api.roles
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'role:delete', NULL)
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (
        api.has_permission(auth.uid(), 'role_assignment:update', NULL)
    );

CREATE POLICY "role assignment delete policy" ON api.role_assignments
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'role_assignment:delete', NULL)
    );
