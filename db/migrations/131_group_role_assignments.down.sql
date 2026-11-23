-- Reverts group_role_assignments: role assignments of users only, global or in
-- a workspace or an OrgUnit subtree. Group, Team-scoped, team-admin and
-- model-invoker assignments, ServiceAccounts and the new permissions go away.

DROP POLICY IF EXISTS "user_profile read policy" ON api.user_profiles;

CREATE POLICY "user_profile read policy" ON api.user_profiles
    FOR SELECT
    USING (
        id = auth.uid()
        OR (SELECT api.has_permission(auth.uid(), 'user_profile:read', NULL))
        OR api.has_org_scoped_permission_on_subject(auth.uid(), 'user_profile:read', id)
    );

DROP TABLE IF EXISTS api.team_application_permissions;
DROP TABLE IF EXISTS api.service_accounts;

DROP POLICY IF EXISTS "team read policy" ON api.teams;
DROP POLICY IF EXISTS "team_member read policy" ON api.team_members;
DROP POLICY IF EXISTS "team_member create policy" ON api.team_members;
DROP POLICY IF EXISTS "team_member delete policy" ON api.team_members;

CREATE POLICY "team read policy" ON api.teams
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'team:read', NULL)));

CREATE POLICY "team_member read policy" ON api.team_members
    FOR SELECT
    USING ((SELECT api.has_permission(auth.uid(), 'team:read', NULL)));

CREATE POLICY "team_member create policy" ON api.team_members
    FOR INSERT
    WITH CHECK ((SELECT api.has_permission(auth.uid(), 'team:assign-member', NULL)));

CREATE POLICY "team_member delete policy" ON api.team_members
    FOR DELETE
    USING ((SELECT api.has_permission(auth.uid(), 'team:assign-member', NULL)));

-- Rows that only the new subjects, scopes or roles allow. Soft-deleted rows
-- that duplicate a live one would block the old indexes; the controller
-- removes them anyway.
DELETE FROM api.role_assignments
WHERE (spec).subject_name IS NOT NULL
   OR (spec).team IS NOT NULL
   OR (spec).role IN ('team-admin', 'model-invoker');

DELETE FROM api.role_assignments ra
WHERE (ra.metadata).deletion_timestamp IS NOT NULL
  AND EXISTS (
    SELECT 1 FROM api.role_assignments o
    WHERE o.id <> ra.id
      AND (o.spec).user_id IS NOT DISTINCT FROM (ra.spec).user_id
      AND COALESCE((o.spec).workspace, '') = COALESCE((ra.spec).workspace, '')
      AND COALESCE((o.spec).org_unit, '') = COALESCE((ra.spec).org_unit, '')
      AND (o.spec).role = (ra.spec).role
  );

DELETE FROM api.roles WHERE (metadata).name IN ('team-admin', 'model-invoker');

DROP FUNCTION IF EXISTS api.update_team_admin_permissions();

UPDATE api.roles
SET spec = ROW((spec).preset_key, ARRAY(
        SELECT p FROM unnest((spec).permissions) AS p
        WHERE p::TEXT NOT IN ('group_policy:read', 'group_policy:create', 'group_policy:update',
                              'group_policy:delete', 'org_unit:usage-read', 'model:invoke',
                              'service_account:read', 'service_account:create',
                              'service_account:update', 'service_account:delete',
                              'team_usage:read')))::api.role_spec
WHERE (spec).permissions && ARRAY['group_policy:read', 'group_policy:create', 'group_policy:update',
                                  'group_policy:delete', 'org_unit:usage-read', 'model:invoke',
                                  'service_account:read', 'service_account:create',
                                  'service_account:update', 'service_account:delete',
                                  'team_usage:read']::api.permission_action[];

DROP FUNCTION IF EXISTS api.get_user_effective_role_assignments(UUID);

CREATE OR REPLACE FUNCTION api.team_members_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_team_id INTEGER;
BEGIN
    IF TG_OP = 'DELETE' THEN
        v_team_id := OLD.team_id;
    ELSE
        v_team_id := NEW.team_id;
    END IF;

    -- The permission check keeps a user without it from learning which Teams
    -- are synced; RLS refuses that user anyway.
    IF auth.uid() IS NOT NULL
        AND pg_trigger_depth() = 1
        AND EXISTS (
            SELECT 1 FROM api.teams t
            WHERE t.id = v_team_id AND (t.spec).identity_source IS NOT NULL
        )
        AND api.has_permission(auth.uid(), 'team:assign-member', NULL) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10290","message": "Members of a Team synced from an identity source are read-only","hint": "Change the group in the directory, or use a Team created in Neutree"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;

    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION api.teams_before_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF auth.uid() IS NOT NULL AND (OLD.spec).identity_source IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10289","message": "A Team synced from an identity source is read-only","hint": "Sync sets it Inactive when the group is gone from the directory"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN OLD;
END;
$$;

DROP TRIGGER IF EXISTS org_unit_members_check_grants ON api.org_unit_members;
DROP FUNCTION IF EXISTS api.org_unit_members_check_grants();
DROP TRIGGER IF EXISTS team_members_check_grants ON api.team_members;
DROP FUNCTION IF EXISTS api.team_members_check_grants();
DROP TRIGGER IF EXISTS validate_role_delegated_grant_on_role_assignments ON api.role_assignments;
DROP FUNCTION IF EXISTS api.role_assignments_check_delegated_grant();
DROP FUNCTION IF EXISTS api.check_role_assignment_grant(UUID, api.role_assignment_spec);
DROP FUNCTION IF EXISTS api.check_role_assignment_delegation(UUID, api.permission_action, api.role_assignment_spec);

DROP POLICY IF EXISTS "role assignment read policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment create policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;
DROP POLICY IF EXISTS "role assignment delete policy" ON api.role_assignments;

CREATE POLICY "role assignment read policy" ON api.role_assignments
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'role_assignment:read', NULL))
        OR api.has_org_scoped_permission_on_subject(auth.uid(), 'role_assignment:read', (spec).user_id)
    );

CREATE POLICY "role assignment create policy" ON api.role_assignments
    FOR INSERT
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'role_assignment:create', NULL))
        OR api.has_org_scoped_permission_on_subject(auth.uid(), 'role_assignment:create', (spec).user_id)
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            (
                (SELECT api.has_permission(auth.uid(), 'role_assignment:update', NULL))
                OR api.has_org_scoped_permission_on_subject(auth.uid(), 'role_assignment:update', (spec).user_id)
            )
            AND (metadata).deletion_timestamp IS NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
        OR
        (
            (
                (SELECT api.has_permission(auth.uid(), 'role_assignment:delete', NULL))
                OR api.has_org_scoped_permission_on_subject(auth.uid(), 'role_assignment:delete', (spec).user_id)
            )
            AND (metadata).deletion_timestamp IS NOT NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
    );

CREATE POLICY "role assignment delete policy" ON api.role_assignments
    FOR DELETE
    USING (
        (
            (SELECT api.has_permission(auth.uid(), 'role_assignment:delete', NULL))
            OR api.has_org_scoped_permission_on_subject(auth.uid(), 'role_assignment:delete', (spec).user_id)
        )
        AND (metadata).name != 'admin-global-role-assignment'
    );

DROP FUNCTION IF EXISTS api.has_permission_on_role_assignment(UUID, api.permission_action, api.role_assignment_spec);
DROP FUNCTION IF EXISTS api.get_team_application_permissions(INTEGER);
DROP FUNCTION IF EXISTS api.has_service_account_scoped_permission(UUID, api.permission_action, UUID);
DROP FUNCTION IF EXISTS api.has_permission_on_service_account(UUID, api.permission_action, UUID);

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

DROP FUNCTION IF EXISTS api.has_permission_on_team(UUID, api.permission_action, INTEGER);
DROP FUNCTION IF EXISTS api.has_team_scoped_permission(UUID, api.permission_action, INTEGER);
DROP FUNCTION IF EXISTS api.check_team_role_binding(UUID, api.permission_action, INTEGER);

CREATE OR REPLACE FUNCTION api.check_org_role_binding(
    user_uuid UUID,
    required_permission api.permission_action,
    org_unit_id INTEGER
)
RETURNS BOOLEAN AS $$
BEGIN
    RETURN FALSE;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

COMMENT ON FUNCTION api.check_org_role_binding(UUID, api.permission_action, INTEGER) IS
    'Extension point of api.has_permission_on_subject / api.has_permission_on_org_unit (OrgUnit-scoped role binding). Default: never grants.';

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

DROP FUNCTION IF EXISTS api.user_role_assignments(UUID);
DROP FUNCTION IF EXISTS api.user_groups(UUID);
DROP FUNCTION IF EXISTS api.team_application_permissions_before_write();
DROP FUNCTION IF EXISTS api.effective_team_application_permissions(INTEGER);
DROP FUNCTION IF EXISTS api.is_management_role(TEXT);
DROP FUNCTION IF EXISTS api.service_accounts_before_write();
DROP FUNCTION IF EXISTS api.refuse_service_account_actor();
DROP FUNCTION IF EXISTS api.is_service_account(UUID);
DROP TYPE IF EXISTS api.service_account_spec;
DROP FUNCTION IF EXISTS api.org_unit_ancestor_ids(TEXT);

DROP INDEX IF EXISTS api.role_assignments_deletion_timestamp_idx;
DROP INDEX IF EXISTS api.role_assignments_subject_idx;
DROP INDEX IF EXISTS api.role_assignments_user_id_idx;
DROP INDEX IF EXISTS api.role_assignment_unique_user_team_role;
DROP INDEX IF EXISTS api.role_assignment_unique_group_subject_role;
DROP INDEX api.role_assignment_unique_user_workspace_role;
DROP INDEX api.role_assignment_unique_user_org_unit_role;

CREATE UNIQUE INDEX role_assignment_unique_user_workspace_role
ON api.role_assignments (((spec).user_id), ((spec).workspace), ((spec).role));

CREATE UNIQUE INDEX role_assignment_unique_user_org_unit_role
ON api.role_assignments (((spec).user_id), ((spec).org_unit), ((spec).role))
WHERE (spec).org_unit IS NOT NULL;

ALTER TYPE api.role_assignment_spec DROP ATTRIBUTE team;
ALTER TYPE api.role_assignment_spec DROP ATTRIBUTE subject_name;
ALTER TYPE api.role_assignment_spec DROP ATTRIBUTE subject_kind;
