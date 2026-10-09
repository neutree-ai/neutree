-- Reverts 112_org_scoped_role_assignments.

-- The department-admin role and every OrgUnit-scoped assignment go away: without
-- spec.org_unit they would be assignments with no scope.
DELETE FROM api.role_assignments
WHERE (spec).role = 'department-admin' OR (spec).org_unit IS NOT NULL;

DELETE FROM api.roles WHERE (metadata).name = 'department-admin';

DROP FUNCTION IF EXISTS api.update_department_admin_permissions();

-- Policies as before (001, 028, 034, 047, 048, 109).
DROP POLICY IF EXISTS "org_unit read policy" ON api.org_units;
DROP POLICY IF EXISTS "org_unit_member read policy" ON api.org_unit_members;

CREATE POLICY "org_unit read policy" ON api.org_units
    FOR SELECT
    USING (api.has_permission(auth.uid(), 'org_unit:read', NULL));

CREATE POLICY "org_unit_member read policy" ON api.org_unit_members
    FOR SELECT
    USING (api.has_permission(auth.uid(), 'org_unit:read', NULL));

DROP POLICY IF EXISTS "role read policy" ON api.roles;

CREATE POLICY "role read policy" ON api.roles
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'role:read', NULL)
    );

DROP POLICY IF EXISTS "role assignment read policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment create policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment delete policy" ON api.role_assignments;

CREATE POLICY "role assignment read policy" ON api.role_assignments
    FOR SELECT
    USING (
        api.has_permission(auth.uid(), 'role_assignment:read', NULL)
    );

CREATE POLICY "role assignment create policy" ON api.role_assignments
    FOR INSERT
    WITH CHECK (
        api.has_permission(auth.uid(), 'role_assignment:create', NULL)
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

CREATE POLICY "role assignment delete policy" ON api.role_assignments
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'role_assignment:delete', NULL)
        AND (metadata).name != 'admin-global-role-assignment'
    );

DROP POLICY IF EXISTS "user_profile read policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile update policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile delete policy" ON api.user_profiles;

CREATE POLICY "user_profile read policy" ON api.user_profiles
    FOR SELECT
    USING (
        id = auth.uid()
        OR
        api.has_permission(auth.uid(), 'user_profile:read', NULL)
    );

CREATE POLICY "user_profile update policy" ON api.user_profiles
    FOR UPDATE
    USING (
        id = auth.uid()
        OR
        api.has_permission(auth.uid(), 'user_profile:update', NULL)
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:delete', NULL)
            AND id != auth.uid()
        )
    )
    WITH CHECK (
        (
            id = auth.uid()
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:update', NULL)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission(auth.uid(), 'user_profile:delete', NULL)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND id != auth.uid()
            AND (metadata).name != 'admin'
        )
    );

CREATE POLICY "user_profile delete policy" ON api.user_profiles
    FOR DELETE
    USING (
        api.has_permission(auth.uid(), 'user_profile:delete', NULL)
        AND id != auth.uid()
        AND (metadata).name != 'admin'
    );

DROP FUNCTION IF EXISTS api.has_permission_on_org_unit(UUID, api.permission_action, INTEGER);
DROP FUNCTION IF EXISTS api.has_permission_on_subject(UUID, api.permission_action, UUID);
DROP FUNCTION IF EXISTS api.check_org_role_binding(UUID, api.permission_action, INTEGER);

DROP INDEX IF EXISTS api.role_assignment_unique_user_org_unit_role;

ALTER TYPE api.role_assignment_spec DROP ATTRIBUTE org_unit;

-- Write checks as before (016, 021).
CREATE OR REPLACE FUNCTION api.validate_role_assignments()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY INVOKER
AS $$
BEGIN
    IF (NEW.spec).user_id IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10032","message": "spec.user_id is required","hint": "Provide User Information"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF (NEW.spec).role IS NULL OR TRIM((NEW.spec).role) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10033","message": "spec.role is required","hint": "Provide Role Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    -- Validation: workspace should not be null or empty when global is false (error code: 10034)
    IF (NEW.spec).global = false AND ((NEW.spec).workspace IS NULL OR TRIM((NEW.spec).workspace) = '') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10034","message": "spec.workspace is required","hint": "Provide Workspace Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

GRANT EXECUTE ON FUNCTION api.validate_role_assignments() TO PUBLIC;

CREATE OR REPLACE FUNCTION api.enforce_global_role_assignment()
RETURNS TRIGGER AS $$
BEGIN
    IF (NEW.spec).global != TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10041","message": "Workspace-level role assignments are not supported","hint": "Use global role assignments"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    NEW.spec := ROW(
        (NEW.spec).user_id,
        NULL,
        TRUE,
        (NEW.spec).role
    )::api.role_assignment_spec;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
