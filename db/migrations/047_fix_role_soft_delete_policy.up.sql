DROP POLICY IF EXISTS "role update policy" ON api.roles;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;

CREATE POLICY "role update policy" ON api.roles
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'role:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
            AND (spec).preset_key IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'role:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND (spec).preset_key IS NULL
        )
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission(auth.uid(), 'role_assignment:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
        OR
        (
            api.has_permission(auth.uid(), 'role_assignment:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
    );
