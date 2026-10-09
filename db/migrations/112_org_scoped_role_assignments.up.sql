-- ----------------------
-- OrgUnit-scoped role assignments and the department-admin role (NEU-839)
-- ----------------------
-- A role assignment can be scoped to an OrgUnit subtree: spec.org_unit is the
-- metadata.name of an OrgUnit. Such an assignment is neither global nor bound
-- to a workspace (spec.global = FALSE, spec.workspace = NULL), and grants its
-- permissions only on the people and OrgUnits of that subtree:
--   * user_profile:*      on users whose primary OrgUnit is in the subtree,
--   * role_assignment:*   on role assignments of those users,
--   * org_unit:read       on the OrgUnits of the subtree and their members,
--   * role:read           on role definitions (needed to pick a role to grant).
-- It grants nothing else: workspace resources (clusters, endpoints, API keys,
-- ...) still go through api.has_permission, which an OrgUnit-scoped
-- assignment never satisfies.
--
-- Checks per target. api.has_permission answers "may this user do X here"
-- for a workspace or the platform. Two entry points next to it answer "may this
-- user do X on that person / that OrgUnit":
--   api.has_permission_on_subject(user_uuid, required_permission, target_user)
--   api.has_permission_on_org_unit(user_uuid, required_permission, org_unit_id)
-- Both pass when api.has_permission passes with no workspace (a global role
-- assignment); otherwise they ask the hook api.check_org_role_binding.
--
-- Extension contract (same as 107_has_permission_hooks):
--   * Community owns the two entry points and the default hook body below.
--   * The default hook grants nothing: community has no OrgUnit scopes, and
--     rejects writing one (api.enforce_global_role_assignment).
--   * The enterprise edition overrides only the hook body (same signature and
--     attributes), and adds its own write checks for delegated grants.
--
-- PostgREST replaces a composite column as a whole on PATCH: a client that
-- writes spec must send every field, org_unit included, or the missing ones
-- become NULL (see NEU-717).

ALTER TYPE api.role_assignment_spec ADD ATTRIBUTE org_unit TEXT;

-- One assignment of a role per user and OrgUnit, like the existing
-- (user_id, workspace, role) index does for workspaces.
CREATE UNIQUE INDEX role_assignment_unique_user_org_unit_role
ON api.role_assignments (((spec).user_id), ((spec).org_unit), ((spec).role))
WHERE (spec).org_unit IS NOT NULL;

-- ----------------------
-- Write checks
-- ----------------------

-- Community only: every role assignment is global. An OrgUnit scope is
-- rejected here, before the assignment would be rewritten to a global one.
-- The enterprise edition drops this trigger.
CREATE OR REPLACE FUNCTION api.enforce_global_role_assignment()
RETURNS TRIGGER AS $$
BEGIN
    IF COALESCE(btrim((NEW.spec).org_unit), '') <> '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10280","message": "OrgUnit-scoped role assignments are not supported","hint": "Use global role assignments"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF (NEW.spec).global != TRUE THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10041","message": "Workspace-level role assignments are not supported","hint": "Use global role assignments"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    NEW.spec := ROW(
        (NEW.spec).user_id,
        NULL,
        TRUE,
        (NEW.spec).role,
        NULL
    )::api.role_assignment_spec;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Required fields and the scope of a role assignment. Exactly one scope:
-- global, a workspace, or an OrgUnit. An OrgUnit scope is stored with
-- global = FALSE and no workspace.
-- SECURITY DEFINER: the OrgUnit lookup must not depend on the writer's
-- org_unit:read.
CREATE OR REPLACE FUNCTION api.validate_role_assignments()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.role_assignment_spec := NEW.spec;
    v_check_org_unit BOOLEAN;
BEGIN
    IF v_spec.user_id IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10032","message": "spec.user_id is required","hint": "Provide User Information"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.role IS NULL OR TRIM(v_spec.role) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10033","message": "spec.role is required","hint": "Provide Role Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.org_unit := NULLIF(btrim(v_spec.org_unit), '');

    IF v_spec.org_unit IS NOT NULL THEN
        IF v_spec.global IS TRUE OR COALESCE(TRIM(v_spec.workspace), '') <> '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10281","message": "An OrgUnit-scoped role assignment is neither global nor in a workspace","hint": "Set spec.global to false and leave spec.workspace empty"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_spec.global := FALSE;
        v_spec.workspace := NULL;

        IF TG_OP = 'INSERT' THEN
            v_check_org_unit := TRUE;
        ELSE
            v_check_org_unit := v_spec.org_unit IS DISTINCT FROM (OLD.spec).org_unit;
        END IF;

        IF v_check_org_unit
            AND NOT EXISTS (SELECT 1 FROM api.org_units u WHERE (u.metadata).name = v_spec.org_unit) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10282","message": "spec.org_unit does not name an OrgUnit","hint": "Use the metadata.name of an OrgUnit"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    ELSIF v_spec.global = FALSE AND (v_spec.workspace IS NULL OR TRIM(v_spec.workspace) = '') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10034","message": "spec.workspace is required","hint": "Provide Workspace Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    NEW.spec := v_spec;

    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION api.validate_role_assignments() FROM PUBLIC;

-- ----------------------
-- Extension point: OrgUnit-scoped role binding
-- ----------------------
-- Does an OrgUnit-scoped role assignment of user_uuid grant
-- required_permission on the OrgUnit org_unit_id? A NULL org_unit_id asks
-- whether such an assignment grants the permission on any OrgUnit.
--
-- Community: there are no OrgUnit scopes, so this never grants. The enterprise
-- edition overrides it. Keep the override in sync when changing the signature.
-- SECURITY DEFINER: the override reads tables under RLS that calls back into
-- the entry points below.
CREATE OR REPLACE FUNCTION api.check_org_role_binding(
    user_uuid UUID,
    required_permission api.permission_action,
    org_unit_id INTEGER
)
RETURNS BOOLEAN AS $$
BEGIN
    RETURN FALSE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

COMMENT ON FUNCTION api.check_org_role_binding(UUID, api.permission_action, INTEGER) IS
    'Extension point of api.has_permission_on_subject / api.has_permission_on_org_unit (OrgUnit-scoped role binding). Community default: never grants; overridden by the enterprise edition.';

-- ----------------------
-- Entry points. Owned by community; the enterprise edition does not override
-- them.
-- ----------------------

-- May user_uuid do required_permission on the user target_user? A global
-- role assignment always counts. Otherwise the OrgUnit-scoped hook decides on
-- the target's primary OrgUnit; a user in no OrgUnit is covered by global
-- assignments only. The API key scope hook runs last, as in api.has_permission.
CREATE OR REPLACE FUNCTION api.has_permission_on_subject(
    user_uuid UUID,
    required_permission api.permission_action,
    target_user UUID
)
RETURNS BOOLEAN AS $$
DECLARE
    v_org_unit_id INTEGER;
BEGIN
    IF api.has_permission(user_uuid, required_permission, NULL) THEN
        RETURN TRUE;
    END IF;

    IF user_uuid IS NULL OR target_user IS NULL THEN
        RETURN FALSE;
    END IF;

    SELECT m.org_unit_id INTO v_org_unit_id
    FROM api.org_unit_members m
    WHERE m.user_id = target_user;

    IF v_org_unit_id IS NULL THEN
        RETURN FALSE;
    END IF;

    IF api.check_org_role_binding(user_uuid, required_permission, v_org_unit_id) IS NOT TRUE THEN
        RETURN FALSE;
    END IF;

    RETURN api.check_api_key_scope(required_permission, NULL) IS TRUE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- May user_uuid do required_permission on the OrgUnit org_unit_id? NULL
-- org_unit_id: on any OrgUnit (used for role:read, which has no target).
CREATE OR REPLACE FUNCTION api.has_permission_on_org_unit(
    user_uuid UUID,
    required_permission api.permission_action,
    org_unit_id INTEGER
)
RETURNS BOOLEAN AS $$
BEGIN
    IF api.has_permission(user_uuid, required_permission, NULL) THEN
        RETURN TRUE;
    END IF;

    IF user_uuid IS NULL THEN
        RETURN FALSE;
    END IF;

    IF api.check_org_role_binding(user_uuid, required_permission, org_unit_id) IS NOT TRUE THEN
        RETURN FALSE;
    END IF;

    RETURN api.check_api_key_scope(required_permission, NULL) IS TRUE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- ----------------------
-- RLS: checks on a person or an OrgUnit go through the new entry points.
-- With only global assignments (community) they decide exactly as before.
-- ----------------------

-- User profiles (034, 048)
DROP POLICY IF EXISTS "user_profile read policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile update policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile delete policy" ON api.user_profiles;

CREATE POLICY "user_profile read policy" ON api.user_profiles
    FOR SELECT
    USING (
        id = auth.uid()
        OR
        api.has_permission_on_subject(auth.uid(), 'user_profile:read', id)
    );

CREATE POLICY "user_profile update policy" ON api.user_profiles
    FOR UPDATE
    USING (
        id = auth.uid()
        OR
        api.has_permission_on_subject(auth.uid(), 'user_profile:update', id)
        OR
        (
            api.has_permission_on_subject(auth.uid(), 'user_profile:delete', id)
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
            api.has_permission_on_subject(auth.uid(), 'user_profile:update', id)
            AND (metadata).deletion_timestamp IS NULL
        )
        OR
        (
            api.has_permission_on_subject(auth.uid(), 'user_profile:delete', id)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND id != auth.uid()
            AND (metadata).name != 'admin'
        )
    );

CREATE POLICY "user_profile delete policy" ON api.user_profiles
    FOR DELETE
    USING (
        api.has_permission_on_subject(auth.uid(), 'user_profile:delete', id)
        AND id != auth.uid()
        AND (metadata).name != 'admin'
    );

-- Role assignments (001, 028, 047): checked on the user the assignment is for.
DROP POLICY IF EXISTS "role assignment read policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment create policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment delete policy" ON api.role_assignments;

CREATE POLICY "role assignment read policy" ON api.role_assignments
    FOR SELECT
    USING (
        api.has_permission_on_subject(auth.uid(), 'role_assignment:read', (spec).user_id)
    );

CREATE POLICY "role assignment create policy" ON api.role_assignments
    FOR INSERT
    WITH CHECK (
        api.has_permission_on_subject(auth.uid(), 'role_assignment:create', (spec).user_id)
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            api.has_permission_on_subject(auth.uid(), 'role_assignment:update', (spec).user_id)
            AND (metadata).deletion_timestamp IS NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
        OR
        (
            api.has_permission_on_subject(auth.uid(), 'role_assignment:delete', (spec).user_id)
            AND (metadata).deletion_timestamp IS NOT NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
    );

CREATE POLICY "role assignment delete policy" ON api.role_assignments
    FOR DELETE
    USING (
        api.has_permission_on_subject(auth.uid(), 'role_assignment:delete', (spec).user_id)
        AND (metadata).name != 'admin-global-role-assignment'
    );

-- Roles (001): reading role definitions also follows an OrgUnit-scoped
-- role:read, so a delegated administrator can pick the role to grant.
DROP POLICY IF EXISTS "role read policy" ON api.roles;

CREATE POLICY "role read policy" ON api.roles
    FOR SELECT
    USING (
        api.has_permission_on_org_unit(auth.uid(), 'role:read', NULL)
    );

-- OrgUnits and their members (109)
DROP POLICY IF EXISTS "org_unit read policy" ON api.org_units;
DROP POLICY IF EXISTS "org_unit_member read policy" ON api.org_unit_members;

CREATE POLICY "org_unit read policy" ON api.org_units
    FOR SELECT
    USING (api.has_permission_on_org_unit(auth.uid(), 'org_unit:read', id));

CREATE POLICY "org_unit_member read policy" ON api.org_unit_members
    FOR SELECT
    USING (api.has_permission_on_org_unit(auth.uid(), 'org_unit:read', org_unit_id));

-- ----------------------
-- Preset role: department-admin
-- ----------------------
-- Manages the people of an OrgUnit subtree when assigned with spec.org_unit.
-- Nothing about it is special-cased: it is a role whose permissions all act on
-- people or OrgUnits. Assigned globally it would act on everyone, like any
-- other role. Its preset_key protects it from changes (028, 047).
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

INSERT INTO api.roles (api_version, kind, metadata, spec)
SELECT 'v1', 'Role',
    ROW('department-admin', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
    ROW('department-admin'::api.role_preset, ARRAY[]::api.permission_action[])::api.role_spec
WHERE NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = 'department-admin');

SELECT api.update_department_admin_permissions();
