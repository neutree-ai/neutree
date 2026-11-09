-- Role assignments held by Teams and OrgUnits and scoped to an OrgUnit subtree
-- or a Team; ServiceAccounts, which hold their Team's application permissions;
-- the team-admin and model-invoker roles.

-- ----------------------
-- Subjects and scopes
-- ----------------------
-- The subject of a role assignment is a user, a Team or an OrgUnit:
--   spec.subject_kind = 'User'    -> spec.user_id names the user;
--   spec.subject_kind = 'Team'    -> spec.subject_name is the Team's metadata.name;
--   spec.subject_kind = 'OrgUnit' -> spec.subject_name is the OrgUnit's metadata.name.
-- A NULL subject_kind is read as 'User'.
--
-- A user holds the union of (api.user_role_assignments):
--   * the assignments of the user;
--   * the assignments of every Active Team the user is in;
--   * the assignments of every Active OrgUnit on the path to the user's
--     primary OrgUnit, which must be Active too (a binding on an OrgUnit
--     covers its whole subtree).
-- Soft-deleted assignments grant nothing.
--
-- An assignment is global, in a workspace, scoped to an OrgUnit subtree
-- (spec.org_unit) or scoped to one Team (spec.team). A Team scope grants only
-- on that Team, through api.has_permission_on_team; api.has_permission and
-- the OrgUnit entry points never count it. Only a holder of a global
-- role_assignment:create may write one: a Team crosses OrgUnits (10314).
--
-- Joining a group is a grant: a user who adds a member (not sync) must be
-- allowed to create each assignment the member gains (10296). Sync writes as
-- service_role with no auth.uid() and is not checked: whoever controls the
-- directory controls group membership.
--
-- spec is a composite that PostgREST replaces as a whole on PATCH. A PATCH
-- whose spec has no subject keeps the stored subject, and one with no scope
-- keeps the stored scope. A row that still has no scope is rejected (10298).
--
-- api.check_team_role_binding is a hook; an override replaces only its body,
-- with the same signature and attributes.

ALTER TYPE api.role_assignment_spec ADD ATTRIBUTE subject_kind TEXT;
ALTER TYPE api.role_assignment_spec ADD ATTRIBUTE subject_name TEXT;
ALTER TYPE api.role_assignment_spec ADD ATTRIBUTE team TEXT;

-- Triggers off: a backfill must not bump update timestamps or run write checks.
ALTER TABLE api.role_assignments DISABLE TRIGGER USER;
UPDATE api.role_assignments SET spec.subject_kind = 'User' WHERE (spec).subject_kind IS NULL;
ALTER TABLE api.role_assignments ENABLE TRIGGER USER;

-- Rows the scope check rejects grant nothing, and every later update of them
-- (the controller's status writes included) would fail.
DELETE FROM api.role_assignments
WHERE (spec).global IS NOT TRUE
  AND COALESCE(btrim((spec).workspace), '') = ''
  AND COALESCE(btrim((spec).org_unit), '') = '';

-- The unique indexes skip soft-deleted rows, so an assignment can be created
-- again before the controller removes the old row. Partial indexes give the
-- planner no statistics on their expressions, so plain indexes on the
-- subject columns stay for that.
DROP INDEX api.role_assignment_unique_user_workspace_role;
DROP INDEX api.role_assignment_unique_user_org_unit_role;

CREATE UNIQUE INDEX role_assignment_unique_user_workspace_role
ON api.role_assignments (((spec).user_id), ((spec).workspace), ((spec).role))
WHERE (metadata).deletion_timestamp IS NULL;

CREATE UNIQUE INDEX role_assignment_unique_user_org_unit_role
ON api.role_assignments (((spec).user_id), ((spec).org_unit), ((spec).role))
WHERE (spec).org_unit IS NOT NULL AND (metadata).deletion_timestamp IS NULL;

CREATE UNIQUE INDEX role_assignment_unique_group_subject_role
ON api.role_assignments (
    ((spec).subject_kind),
    ((spec).subject_name),
    (COALESCE((spec).workspace, '')),
    (COALESCE((spec).org_unit, '')),
    (COALESCE((spec).team, '')),
    ((spec).role)
)
WHERE (metadata).deletion_timestamp IS NULL;

CREATE UNIQUE INDEX role_assignment_unique_user_team_role
ON api.role_assignments (((spec).user_id), ((spec).team), ((spec).role))
WHERE (spec).team IS NOT NULL AND (metadata).deletion_timestamp IS NULL;

CREATE INDEX role_assignments_user_id_idx
ON api.role_assignments (((spec).user_id));

CREATE INDEX role_assignments_subject_idx
ON api.role_assignments (((spec).subject_kind), ((spec).subject_name));

-- Statistics only: without them the planner takes "not deleted" for a rare
-- case and joins group assignments by nested scans.
CREATE INDEX role_assignments_deletion_timestamp_idx
ON api.role_assignments (((metadata).deletion_timestamp));

-- The ids of the OrgUnits on an OrgUnit path, root first, the OrgUnit last.
CREATE OR REPLACE FUNCTION api.org_unit_ancestor_ids(path TEXT)
RETURNS INTEGER[]
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT string_to_array(btrim(path, '/'), '/')::INTEGER[];
$$;

-- ----------------------
-- ServiceAccounts
-- ----------------------
-- A ServiceAccount (SA) is a principal of one Team, for applications. It is
-- not a user: it has no auth.users row and no login, it is in no Team or
-- OrgUnit as a member, and it is never listed among users. Its id shares the
-- principal id space with users: an SA key carries it as its user, and it is
-- auth.uid() on the control plane.
--
-- service_account:* on the Team (api.has_permission_on_team) governs the SA.
-- An SA holds no role assignments (10326): every enabled SA of an Active Team
-- holds the Team's application permissions (api.team_application_permissions).
-- A Team with SAs cannot be deleted (10315).

CREATE TYPE api.service_account_spec AS (
    description TEXT,
    disabled BOOLEAN
);

CREATE TABLE api.service_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    api_version TEXT NOT NULL DEFAULT 'v1',
    kind TEXT NOT NULL DEFAULT 'ServiceAccount',
    metadata api.metadata,
    spec api.service_account_spec,
    team_id INTEGER NOT NULL REFERENCES api.teams (id),
    created_by UUID REFERENCES api.user_profiles (id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX service_accounts_team_name_idx ON api.service_accounts (team_id, ((metadata).name));

CREATE TRIGGER update_service_accounts_update_timestamp
    BEFORE UPDATE ON api.service_accounts
    FOR EACH ROW
    EXECUTE FUNCTION update_metadata_update_timestamp_column();

CREATE TRIGGER set_service_accounts_default_timestamp
    BEFORE INSERT ON api.service_accounts
    FOR EACH ROW
    EXECUTE FUNCTION set_default_metadata_timestamp_column();

CREATE TRIGGER validate_name_on_service_accounts
    BEFORE INSERT OR UPDATE ON api.service_accounts
    FOR EACH ROW
    EXECUTE FUNCTION api.validate_metadata_name();

-- Is p_id a ServiceAccount? For checks that treat users and SAs apart.
CREATE OR REPLACE FUNCTION api.is_service_account(p_id UUID)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (SELECT 1 FROM api.service_accounts s WHERE s.id = p_id);
$$;

-- An SA acts for no user, so it cannot create SAs or API keys: created_by
-- names a user.
CREATE OR REPLACE FUNCTION api.refuse_service_account_actor()
RETURNS VOID
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF auth.uid() IS NOT NULL AND api.is_service_account(auth.uid()) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10320","message": "A ServiceAccount cannot create ServiceAccounts or API keys","hint": "Use the account of a user"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION api.service_accounts_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    NEW.spec := ROW(COALESCE((NEW.spec).description, ''), COALESCE((NEW.spec).disabled, FALSE))::api.service_account_spec;

    IF TG_OP = 'INSERT' THEN
        PERFORM api.refuse_service_account_actor();

        IF EXISTS (SELECT 1 FROM auth.users u WHERE u.id = NEW.id) THEN
            RAISE EXCEPTION 'id % is taken by a user', NEW.id USING ERRCODE = '23505';
        END IF;

        IF NOT EXISTS (SELECT 1 FROM api.teams t WHERE t.id = NEW.team_id AND (t.status).phase = 'Active') THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10317","message": "ServiceAccounts and their API keys can only be created for an Active Team","hint": "The Team is gone from its directory"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        NEW.created_by := COALESCE(auth.uid(), NEW.created_by);
    ELSIF NEW.id IS DISTINCT FROM OLD.id
        OR NEW.team_id IS DISTINCT FROM OLD.team_id
        -- ON DELETE SET NULL of created_by is the one change allowed.
        OR (auth.uid() IS NOT NULL AND NEW.created_by IS NOT NULL
            AND NEW.created_by IS DISTINCT FROM OLD.created_by) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10318","message": "The Team of a ServiceAccount and the owner of an API key cannot be changed","hint": "Create a new one instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER service_accounts_before_write
    BEFORE INSERT OR UPDATE ON api.service_accounts
    FOR EACH ROW
    EXECUTE FUNCTION api.service_accounts_before_write();

REVOKE ALL ON FUNCTION api.service_accounts_before_write() FROM PUBLIC;

-- ----------------------
-- Team application permissions
-- ----------------------
-- Every enabled SA of an Active Team holds these roles in these workspaces
-- (NULL: every workspace). A Team without a row holds model-invoker in every
-- workspace. A role that manages access (roles, role assignments, users,
-- identity sources, departments, Teams, group policies, applications, or
-- system:admin) would let an application manage access itself, so it may not
-- be one of them (10325); a role edited into one later stops counting there.

CREATE TABLE api.team_application_permissions (
    team_id INTEGER PRIMARY KEY REFERENCES api.teams (id) ON DELETE CASCADE,
    roles TEXT[] NOT NULL DEFAULT '{}',
    workspaces TEXT[],
    update_timestamp TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE api.team_application_permissions IS
    'Application permissions of a Team: every enabled ServiceAccount of the Team holds these roles in these workspaces (NULL: globally). No row: model-invoker globally.';

CREATE OR REPLACE FUNCTION api.is_management_role(p_role TEXT)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT EXISTS (
        SELECT 1 FROM api.roles r
        WHERE (r.metadata).name = p_role
          AND (r.spec).permissions && ARRAY[
              'system:admin',
              'role:create', 'role:update', 'role:delete',
              'role_assignment:create', 'role_assignment:update', 'role_assignment:delete',
              'user_profile:create', 'user_profile:update', 'user_profile:delete',
              'identity_source:create', 'identity_source:update', 'identity_source:delete',
              'org_unit:assign-member',
              'team:create', 'team:update', 'team:delete', 'team:assign-member',
              'group_policy:create', 'group_policy:update', 'group_policy:delete',
              'service_account:create', 'service_account:update', 'service_account:delete'
          ]::api.permission_action[]
    );
$$;

COMMENT ON FUNCTION api.is_management_role(TEXT) IS
    'Does the role manage access (roles, assignments, users, identity sources, departments, Teams, group policies, applications, system:admin)? Such a role may not be a Team application permission.';

CREATE OR REPLACE FUNCTION api.effective_team_application_permissions(p_team_id INTEGER)
RETURNS TABLE (roles TEXT[], workspaces TEXT[], is_default BOOLEAN)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT ARRAY(
               SELECT r FROM unnest(COALESCE(c.roles, ARRAY['model-invoker']::TEXT[])) AS r
               WHERE api.is_management_role(r) IS NOT TRUE
               ORDER BY 1),
           c.workspaces,
           c.team_id IS NULL
    FROM api.teams t
    LEFT JOIN api.team_application_permissions c ON c.team_id = t.id
    WHERE t.id = p_team_id;
$$;

REVOKE ALL ON FUNCTION api.effective_team_application_permissions(INTEGER) FROM PUBLIC;

CREATE OR REPLACE FUNCTION api.team_application_permissions_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_role TEXT;
BEGIN
    NEW.roles := ARRAY(SELECT DISTINCT btrim(r) FROM unnest(NEW.roles) AS r ORDER BY 1);

    IF NEW.workspaces IS NOT NULL THEN
        NEW.workspaces := ARRAY(SELECT DISTINCT btrim(w) FROM unnest(NEW.workspaces) AS w ORDER BY 1);
    END IF;

    IF EXISTS (
        SELECT 1 FROM unnest(NEW.roles) AS r
        WHERE NOT EXISTS (
            SELECT 1 FROM api.roles x
            WHERE (x.metadata).name = r AND (x.metadata).deletion_timestamp IS NULL))
        OR EXISTS (
        SELECT 1 FROM unnest(NEW.workspaces) AS w
        WHERE NOT EXISTS (
            SELECT 1 FROM api.workspaces x
            WHERE (x.metadata).name = w AND (x.metadata).deletion_timestamp IS NULL)) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10324","message": "Team application permissions name a role or workspace that does not exist","hint": "Use the metadata.name of existing roles and workspaces"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    SELECT r INTO v_role FROM unnest(NEW.roles) AS r WHERE api.is_management_role(r) LIMIT 1;
    IF v_role IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10325","message": "Role ''%s'' manages access and cannot be a Team application permission","hint": "An application permission may not hold role, role assignment, user, identity source, department, Team, group policy or application management permissions, or system:admin"}', v_role),
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    NEW.update_timestamp := now();

    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION api.team_application_permissions_before_write() FROM PUBLIC;

CREATE TRIGGER team_application_permissions_before_write
    BEFORE INSERT OR UPDATE ON api.team_application_permissions
    FOR EACH ROW
    EXECUTE FUNCTION api.team_application_permissions_before_write();

-- ----------------------
-- Effective assignments
-- ----------------------

-- The groups of a user, or of an SA: its one group is its Team.
CREATE OR REPLACE FUNCTION api.user_groups(user_uuid UUID)
RETURNS TABLE (kind TEXT, name TEXT, active BOOLEAN)
LANGUAGE sql
STABLE
AS $$
    SELECT 'Team'::TEXT, (t.metadata).name, COALESCE((t.status).phase = 'Active', FALSE)
    FROM api.teams t
    WHERE t.id = ANY (ARRAY(
            SELECT tm.team_id FROM api.team_members tm WHERE tm.user_id = user_uuid))
    UNION ALL
    SELECT 'OrgUnit'::TEXT, (s.metadata).name, COALESCE((s.status).phase = 'Active' AND p.active, FALSE)
    FROM (
        SELECT api.org_unit_ancestor_ids(u.path) AS ids,
               (u.status).phase = 'Active' AS active
        FROM api.org_unit_members m
        JOIN api.org_units u ON u.id = m.org_unit_id
        WHERE m.user_id = user_uuid
    ) p
    JOIN api.org_units s ON s.id = ANY (p.ids)
    UNION ALL
    SELECT 'Team'::TEXT, (t.metadata).name, COALESCE((t.status).phase = 'Active', FALSE)
    FROM api.service_accounts sa
    JOIN api.teams t ON t.id = sa.team_id
    WHERE sa.id = user_uuid;
$$;

REVOKE ALL ON FUNCTION api.user_groups(UUID) FROM PUBLIC;

-- The only place that expands groups; every role-binding hook reads through
-- it. Plain SQL, so it inlines into the (SECURITY DEFINER) hooks and their
-- cached plans; nobody else may execute it. An SA's rows are synthesized from
-- its Team's application permissions: no id, subject Team.
CREATE OR REPLACE FUNCTION api.user_role_assignments(user_uuid UUID)
RETURNS SETOF api.role_assignments
LANGUAGE sql
STABLE
AS $$
    WITH groups AS (
        SELECT g.kind, g.name FROM api.user_groups(user_uuid) g WHERE g.active
    )
    SELECT ra.*
    FROM api.role_assignments ra
    WHERE (ra.spec).user_id = user_uuid
      AND (ra.metadata).deletion_timestamp IS NULL
    UNION ALL
    SELECT ra.*
    FROM unnest(ARRAY(
            SELECT g.name FROM groups g
            WHERE g.kind = 'Team'
              AND NOT (SELECT api.is_service_account(user_uuid)))) AS g(name)
    JOIN api.role_assignments ra
      ON (ra.spec).subject_kind = 'Team'
     AND (ra.spec).subject_name = g.name
    WHERE (ra.metadata).deletion_timestamp IS NULL
    UNION ALL
    SELECT ra.*
    FROM unnest(ARRAY(
            SELECT g.name FROM groups g WHERE g.kind = 'OrgUnit')) AS g(name)
    JOIN api.role_assignments ra
      ON (ra.spec).subject_kind = 'OrgUnit'
     AND (ra.spec).subject_name = g.name
    WHERE (ra.metadata).deletion_timestamp IS NULL
    UNION ALL
    SELECT NULL::INTEGER, 'v1'::TEXT, 'RoleAssignment'::TEXT, NULL::api.metadata,
           ROW(sa.id, w.ws, w.ws IS NULL, r.role, NULL, 'Team', (t.metadata).name, NULL)::api.role_assignment_spec,
           NULL::api.role_assignment_status
    FROM api.service_accounts sa
    JOIN api.teams t ON t.id = sa.team_id
    CROSS JOIN LATERAL api.effective_team_application_permissions(t.id) c
    CROSS JOIN LATERAL unnest(c.roles) AS r(role)
    CROSS JOIN LATERAL unnest(COALESCE(c.workspaces, ARRAY[NULL]::TEXT[])) AS w(ws)
    WHERE sa.id = user_uuid
      AND (sa.spec).disabled IS NOT TRUE
      AND (t.status).phase = 'Active';
$$;

REVOKE ALL ON FUNCTION api.user_role_assignments(UUID) FROM PUBLIC;

-- Role-binding hook (api.check_role_binding), through the group expansion.
-- force_generic_plan: RLS calls this once per row, and planning the expansion
-- again for each call cost ten times running it.
CREATE OR REPLACE FUNCTION api.check_role_binding(
    user_uuid UUID,
    required_permission api.permission_action,
    workspace TEXT DEFAULT NULL
)
RETURNS BOOLEAN
SET plan_cache_mode = force_generic_plan
AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1
        FROM api.user_role_assignments(user_uuid) ra
        JOIN api.roles r ON (ra.spec).role = (r.metadata).name
        WHERE (ra.spec).global = TRUE
          AND required_permission = ANY((r.spec).permissions)
    );
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

COMMENT ON FUNCTION api.check_role_binding(UUID, api.permission_action, TEXT) IS
    'Extension point of api.has_permission (role binding).';

-- TRUE when an effective assignment of user_uuid scoped to an Active OrgUnit S
-- grants required_permission and org_unit_id is an Active OrgUnit in S's
-- subtree. A NULL org_unit_id asks for any such assignment.
-- force_generic_plan as in api.check_role_binding: RLS calls this once per row.
CREATE OR REPLACE FUNCTION api.check_org_role_binding(
    user_uuid UUID,
    required_permission api.permission_action,
    org_unit_id INTEGER
)
RETURNS BOOLEAN
SET plan_cache_mode = force_generic_plan
AS $$
DECLARE
    v_target_path TEXT COLLATE "C";
BEGIN
    IF check_org_role_binding.org_unit_id IS NULL THEN
        RETURN EXISTS (
            SELECT 1
            FROM api.user_role_assignments(check_org_role_binding.user_uuid) ra
            JOIN api.roles r ON (r.metadata).name = (ra.spec).role
            JOIN api.org_units s ON (s.metadata).name = (ra.spec).org_unit
            WHERE (s.status).phase = 'Active'
              AND check_org_role_binding.required_permission = ANY((r.spec).permissions)
        );
    END IF;

    SELECT t.path INTO v_target_path
    FROM api.org_units t
    WHERE t.id = check_org_role_binding.org_unit_id
      AND (t.status).phase = 'Active';

    IF v_target_path IS NULL THEN
        RETURN FALSE;
    END IF;

    RETURN EXISTS (
        SELECT 1
        FROM api.user_role_assignments(check_org_role_binding.user_uuid) ra
        JOIN api.roles r ON (r.metadata).name = (ra.spec).role
        JOIN api.org_units s ON (s.metadata).name = (ra.spec).org_unit
        WHERE (s.status).phase = 'Active'
          AND v_target_path >= s.path
          AND v_target_path < s.path || ':'
          AND check_org_role_binding.required_permission = ANY((r.spec).permissions)
    );
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

COMMENT ON FUNCTION api.check_org_role_binding(UUID, api.permission_action, INTEGER) IS
    'Extension point of api.has_permission_on_subject / api.has_permission_on_org_unit (OrgUnit-scoped role binding). Grants on the subtree of an Active OrgUnit.';

-- ----------------------
-- Extension point: Team-scoped role binding
-- ----------------------
-- Does an effective assignment of user_uuid scoped to the Team team_id grant
-- required_permission? A NULL team_id asks for any Team. Only Active Teams
-- count, as for OrgUnits.
-- force_generic_plan as in api.check_role_binding: RLS calls this once per row.
CREATE OR REPLACE FUNCTION api.check_team_role_binding(
    user_uuid UUID,
    required_permission api.permission_action,
    team_id INTEGER
)
RETURNS BOOLEAN
SET plan_cache_mode = force_generic_plan
AS $$
BEGIN
    RETURN EXISTS (
        SELECT 1
        FROM api.user_role_assignments(check_team_role_binding.user_uuid) ra
        JOIN api.roles r ON (r.metadata).name = (ra.spec).role
        JOIN api.teams t ON (t.metadata).name = (ra.spec).team
        WHERE (t.status).phase = 'Active'
          AND (check_team_role_binding.team_id IS NULL OR t.id = check_team_role_binding.team_id)
          AND check_team_role_binding.required_permission = ANY((r.spec).permissions)
    );
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

COMMENT ON FUNCTION api.check_team_role_binding(UUID, api.permission_action, INTEGER) IS
    'Extension point of api.has_permission_on_team (Team-scoped role binding). Grants on an Active Team.';

-- The Team-scoped part of api.has_permission_on_team, without the global
-- check, as api.has_org_scoped_permission is for OrgUnits: the hook decides,
-- then the API key scope hook runs. RLS policies call it per row next to a
-- global check that runs once per statement.
CREATE OR REPLACE FUNCTION api.has_team_scoped_permission(
    user_uuid UUID,
    required_permission api.permission_action,
    team_id INTEGER
)
RETURNS BOOLEAN AS $$
BEGIN
    IF user_uuid IS NULL THEN
        RETURN FALSE;
    END IF;

    IF api.check_team_role_binding(user_uuid, required_permission, team_id) IS NOT TRUE THEN
        RETURN FALSE;
    END IF;

    RETURN api.check_api_key_scope(required_permission, NULL) IS TRUE;
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

-- May user_uuid do required_permission on the Team team_id? A global role
-- assignment always counts; otherwise the Team-scoped hook decides. NULL
-- team_id: on any Team.
CREATE OR REPLACE FUNCTION api.has_permission_on_team(
    user_uuid UUID,
    required_permission api.permission_action,
    team_id INTEGER
)
RETURNS BOOLEAN AS $$
BEGIN
    RETURN api.has_permission(user_uuid, required_permission, NULL) IS TRUE
        OR api.has_team_scoped_permission(user_uuid, required_permission, team_id);
END;
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER;

-- Any OrgUnit or Team scope counts.
CREATE OR REPLACE FUNCTION api.has_permission_in_any_scope(
    user_uuid UUID,
    required_permission api.permission_action
)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
AS $$
    SELECT api.has_permission_on_org_unit(user_uuid, required_permission, NULL) IS TRUE
        OR api.has_permission_on_team(user_uuid, required_permission, NULL) IS TRUE;
$$;

-- May user_uuid do required_permission on the SA p_service_account_id? It is
-- decided on the SA's Team. FALSE for an unknown SA: a NULL Team would mean
-- "any Team" to the entry point.
CREATE OR REPLACE FUNCTION api.has_permission_on_service_account(
    user_uuid UUID,
    required_permission api.permission_action,
    p_service_account_id UUID
)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_team_id INTEGER;
BEGIN
    SELECT s.team_id INTO v_team_id FROM api.service_accounts s WHERE s.id = p_service_account_id;

    IF v_team_id IS NULL THEN
        RETURN FALSE;
    END IF;

    RETURN api.has_permission_on_team(user_uuid, required_permission, v_team_id) IS TRUE;
END;
$$;

-- The Team-scoped part of api.has_permission_on_service_account, for RLS next
-- to a global check that runs once per statement.
CREATE OR REPLACE FUNCTION api.has_service_account_scoped_permission(
    user_uuid UUID,
    required_permission api.permission_action,
    p_service_account_id UUID
)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
    SELECT COALESCE((
        SELECT api.has_team_scoped_permission(user_uuid, required_permission, s.team_id)
        FROM api.service_accounts s
        WHERE s.id = p_service_account_id), FALSE);
$$;

-- For the UI: the application permissions of a Team, for readers of its row.
CREATE OR REPLACE FUNCTION api.get_team_application_permissions(p_team_id INTEGER)
RETURNS TABLE (roles TEXT[], workspaces TEXT[], is_default BOOLEAN)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF p_team_id IS NULL
        OR (api.has_permission(auth.uid(), 'role_assignment:read', NULL) IS NOT TRUE
            AND api.has_permission_on_team(auth.uid(), 'service_account:read', p_team_id) IS NOT TRUE) THEN
        RAISE insufficient_privilege
            USING message = 'service_account:read on the Team or global role_assignment:read is required to read its application permissions';
    END IF;

    RETURN QUERY SELECT * FROM api.effective_team_application_permissions(p_team_id);
END;
$$;

-- ----------------------
-- Who may read and write an assignment, by subject
-- ----------------------
-- User: on that user (global, or a subtree holding the user).
-- OrgUnit: on that OrgUnit (global, or a subtree holding it).
-- Team: global only; a Team crosses OrgUnits.
-- The policies check a global assignment once per statement first.
CREATE OR REPLACE FUNCTION api.has_permission_on_role_assignment(
    user_uuid UUID,
    required_permission api.permission_action,
    p_spec api.role_assignment_spec
)
RETURNS BOOLEAN
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_org_unit_id INTEGER;
BEGIN
    CASE COALESCE(p_spec.subject_kind, 'User')
    WHEN 'User' THEN
        RETURN api.has_permission_on_subject(user_uuid, required_permission, p_spec.user_id) IS TRUE;
    WHEN 'OrgUnit' THEN
        SELECT u.id INTO v_org_unit_id
        FROM api.org_units u
        WHERE (u.metadata).name = p_spec.subject_name;

        -- A NULL id would mean "any OrgUnit" to the entry point.
        IF v_org_unit_id IS NULL THEN
            RETURN api.has_permission(user_uuid, required_permission, NULL) IS TRUE;
        END IF;

        RETURN api.has_permission_on_org_unit(user_uuid, required_permission, v_org_unit_id) IS TRUE;
    ELSE
        RETURN api.has_permission(user_uuid, required_permission, NULL) IS TRUE;
    END CASE;
END;
$$;

DROP POLICY IF EXISTS "role assignment read policy" ON api.role_assignments;

DROP POLICY IF EXISTS "role assignment create policy" ON api.role_assignments;

DROP POLICY IF EXISTS "role assignment update policy" ON api.role_assignments;

DROP POLICY IF EXISTS "role assignment delete policy" ON api.role_assignments;

CREATE POLICY "role assignment read policy" ON api.role_assignments
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'role_assignment:read', NULL))
        OR api.has_permission_on_role_assignment(auth.uid(), 'role_assignment:read', spec)
    );

CREATE POLICY "role assignment create policy" ON api.role_assignments
    FOR INSERT
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'role_assignment:create', NULL))
        OR api.has_permission_on_role_assignment(auth.uid(), 'role_assignment:create', spec)
    );

CREATE POLICY "role assignment update policy" ON api.role_assignments
    FOR UPDATE
    USING (true)
    WITH CHECK (
        (
            (
                (SELECT api.has_permission(auth.uid(), 'role_assignment:update', NULL))
                OR api.has_permission_on_role_assignment(auth.uid(), 'role_assignment:update', spec)
            )
            AND (metadata).deletion_timestamp IS NULL
            AND (metadata).name != 'admin-global-role-assignment'
        )
        OR
        (
            (
                (SELECT api.has_permission(auth.uid(), 'role_assignment:delete', NULL))
                OR api.has_permission_on_role_assignment(auth.uid(), 'role_assignment:delete', spec)
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
            OR api.has_permission_on_role_assignment(auth.uid(), 'role_assignment:delete', spec)
        )
        AND (metadata).name != 'admin-global-role-assignment'
    );

-- ----------------------
-- Write checks
-- ----------------------

-- A Team scope passes through, and the rewrite keeps spec.team.
CREATE OR REPLACE FUNCTION api.enforce_global_role_assignment()
RETURNS TRIGGER AS $$
BEGIN
    -- api.validate_role_assignments checks an OrgUnit or Team scope.
    IF COALESCE(btrim((NEW.spec).org_unit), '') <> ''
        OR COALESCE(btrim((NEW.spec).team), '') <> '' THEN
        RETURN NEW;
    END IF;

    -- A PATCH without a scope keeps the stored one; rewriting it to
    -- global here would widen a scoped assignment.
    IF TG_OP = 'UPDATE'
        AND (NEW.spec).global IS NULL
        AND COALESCE(btrim((NEW.spec).workspace), '') = '' THEN
        RETURN NEW;
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
        NULL,
        (NEW.spec).subject_kind,
        (NEW.spec).subject_name,
        NULL
    )::api.role_assignment_spec;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ServiceAccounts hold no role assignments (10326).
CREATE OR REPLACE FUNCTION api.validate_role_assignments()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_spec api.role_assignment_spec := NEW.spec;
    v_check_scope BOOLEAN;
    v_subject_changed BOOLEAN;
BEGIN
    v_spec.subject_kind := NULLIF(btrim(v_spec.subject_kind), '');
    v_spec.subject_name := NULLIF(btrim(v_spec.subject_name), '');

    IF TG_OP = 'UPDATE'
        AND v_spec.user_id IS NULL
        AND v_spec.subject_kind IS NULL
        AND v_spec.subject_name IS NULL THEN
        v_spec.user_id := (OLD.spec).user_id;
        v_spec.subject_kind := (OLD.spec).subject_kind;
        v_spec.subject_name := (OLD.spec).subject_name;
    END IF;

    v_spec.subject_kind := COALESCE(v_spec.subject_kind, 'User');

    -- A ServiceAccount takes its roles from its Team (api.user_role_assignments).
    IF v_spec.subject_kind = 'ServiceAccount' OR api.is_service_account(v_spec.user_id) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10326","message": "A ServiceAccount holds no role assignments","hint": "Set the application permissions of its Team instead"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.subject_kind NOT IN ('User', 'Team', 'OrgUnit') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10292","message": "spec.subject_kind must be User, Team or OrgUnit","hint": "Leave it empty for a user"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.subject_kind = 'User' THEN
        IF v_spec.user_id IS NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10032","message": "spec.user_id is required","hint": "Provide User Information"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        IF v_spec.subject_name IS NOT NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10293","message": "A User subject has spec.user_id and no spec.subject_name","hint": "Set spec.subject_kind to Team or OrgUnit to assign a group"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    ELSIF v_spec.subject_name IS NULL OR v_spec.user_id IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10293","message": "A Team or OrgUnit subject has spec.subject_name and no spec.user_id","hint": "Set spec.subject_name to the metadata.name of the group"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.role IS NULL OR TRIM(v_spec.role) = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10033","message": "spec.role is required","hint": "Provide Role Name"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF TG_OP = 'INSERT' THEN
        v_subject_changed := TRUE;
    ELSE
        v_subject_changed := v_spec.subject_kind IS DISTINCT FROM COALESCE((OLD.spec).subject_kind, 'User')
            OR v_spec.subject_name IS DISTINCT FROM (OLD.spec).subject_name;
    END IF;

    IF v_subject_changed AND (
        (v_spec.subject_kind = 'Team'
            AND NOT EXISTS (SELECT 1 FROM api.teams t WHERE (t.metadata).name = v_spec.subject_name))
        OR (v_spec.subject_kind = 'OrgUnit'
            AND NOT EXISTS (SELECT 1 FROM api.org_units u WHERE (u.metadata).name = v_spec.subject_name))
    ) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10294","message": "spec.subject_name does not name a group of spec.subject_kind","hint": "Use the metadata.name of a Team or an OrgUnit"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    v_spec.org_unit := NULLIF(btrim(v_spec.org_unit), '');
    v_spec.team := NULLIF(btrim(v_spec.team), '');

    IF TG_OP = 'UPDATE'
        AND v_spec.global IS NULL
        AND COALESCE(btrim(v_spec.workspace), '') = ''
        AND v_spec.org_unit IS NULL
        AND v_spec.team IS NULL THEN
        v_spec.global := (OLD.spec).global;
        v_spec.workspace := (OLD.spec).workspace;
        v_spec.org_unit := NULLIF(btrim((OLD.spec).org_unit), '');
        v_spec.team := NULLIF(btrim((OLD.spec).team), '');
    END IF;

    IF v_spec.global IS NULL
        AND COALESCE(btrim(v_spec.workspace), '') = ''
        AND v_spec.org_unit IS NULL
        AND v_spec.team IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10298","message": "A role assignment needs a scope","hint": "Set spec.global to true, or set spec.workspace, spec.org_unit or spec.team"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_spec.team IS NOT NULL THEN
        IF v_spec.global IS TRUE
            OR COALESCE(TRIM(v_spec.workspace), '') <> ''
            OR v_spec.org_unit IS NOT NULL THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10312","message": "A Team-scoped role assignment is neither global, in a workspace nor OrgUnit-scoped","hint": "Set spec.global to false and leave spec.workspace and spec.org_unit empty"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_spec.global := FALSE;
        v_spec.workspace := NULL;

        IF TG_OP = 'INSERT' THEN
            v_check_scope := TRUE;
        ELSE
            v_check_scope := v_spec.team IS DISTINCT FROM (OLD.spec).team;
        END IF;

        IF v_check_scope
            AND NOT EXISTS (SELECT 1 FROM api.teams t WHERE (t.metadata).name = v_spec.team) THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10313","message": "spec.team does not name a Team","hint": "Use the metadata.name of a Team"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    ELSIF v_spec.org_unit IS NOT NULL THEN
        IF v_spec.global IS TRUE OR COALESCE(TRIM(v_spec.workspace), '') <> '' THEN
            RAISE sqlstate 'PGRST'
                USING message = '{"code": "10281","message": "An OrgUnit-scoped role assignment is neither global nor in a workspace","hint": "Set spec.global to false and leave spec.workspace empty"}',
                detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;

        v_spec.global := FALSE;
        v_spec.workspace := NULL;

        IF TG_OP = 'INSERT' THEN
            v_check_scope := TRUE;
        ELSE
            v_check_scope := v_spec.org_unit IS DISTINCT FROM (OLD.spec).org_unit;
        END IF;

        IF v_check_scope
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

-- A Team scope is refused first: no OrgUnit scope covers a Team.
CREATE OR REPLACE FUNCTION api.check_role_assignment_delegation(
    p_actor UUID,
    p_action api.permission_action,
    p_spec api.role_assignment_spec
)
RETURNS VOID
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_kind TEXT := COALESCE(p_spec.subject_kind, 'User');
    v_target_path TEXT COLLATE "C";
    v_scope_path TEXT COLLATE "C";
    v_role_perms api.permission_action[];
    v_held api.permission_action[];
BEGIN
    IF COALESCE(btrim(p_spec.team), '') <> '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10314","message": "Only a global administrator can grant or revoke Team-scoped role assignments","hint": "Team-scoped role assignments need a global role assignment"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_kind NOT IN ('User', 'OrgUnit') THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10297","message": "A delegated administrator can only grant or revoke role assignments of users and OrgUnits","hint": "Role assignments of a Team need a global role assignment"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF p_spec.global IS TRUE
        OR COALESCE(btrim(p_spec.workspace), '') <> ''
        OR COALESCE(btrim(p_spec.org_unit), '') = '' THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10283","message": "A delegated administrator can only grant or revoke OrgUnit-scoped role assignments","hint": "Global and workspace role assignments need a global role assignment"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF v_kind = 'OrgUnit' THEN
        SELECT u.path INTO v_target_path
        FROM api.org_units u
        WHERE (u.metadata).name = p_spec.subject_name
          AND (u.status).phase = 'Active';
    ELSE
        SELECT u.path INTO v_target_path
        FROM api.org_unit_members m
        JOIN api.org_units u ON u.id = m.org_unit_id
        WHERE m.user_id = p_spec.user_id
          AND (u.status).phase = 'Active';
    END IF;

    SELECT u.path INTO v_scope_path
    FROM api.org_units u
    WHERE (u.metadata).name = btrim(p_spec.org_unit)
      AND (u.status).phase = 'Active';

    IF v_target_path IS NULL OR v_scope_path IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10284","message": "The user or the OrgUnit of the role assignment is outside your OrgUnit scope","hint": "Both must be in an active OrgUnit you manage"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    SELECT COALESCE((r.spec).permissions, ARRAY[]::api.permission_action[]) INTO v_role_perms
    FROM api.roles r
    WHERE (r.metadata).name = p_spec.role;

    IF v_role_perms IS NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10285","message": "spec.role does not name a role","hint": "Use the metadata.name of a role"}',
            detail = '{"status": 400, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    SELECT COALESCE(array_agg(DISTINCT p), ARRAY[]::api.permission_action[]) INTO v_held
    FROM api.user_role_assignments(p_actor) ra
    JOIN api.roles r ON (r.metadata).name = (ra.spec).role
    JOIN api.org_units s ON (s.metadata).name = (ra.spec).org_unit
    CROSS JOIN LATERAL unnest((r.spec).permissions) AS p
    WHERE (s.status).phase = 'Active'
      AND v_target_path >= s.path AND v_target_path < s.path || ':'
      AND v_scope_path >= s.path AND v_scope_path < s.path || ':';

    IF NOT (p_action = ANY(v_held)) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10284","message": "The user or the OrgUnit of the role assignment is outside your OrgUnit scope","hint": "Both must be in an active OrgUnit you manage"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    IF NOT (v_role_perms <@ v_held) THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10286","message": "The role grants permissions you do not hold on this OrgUnit scope","hint": "Grant a role whose permissions you hold yourself"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;
END;
$$;

REVOKE ALL ON FUNCTION api.check_role_assignment_delegation(UUID, api.permission_action, api.role_assignment_spec) FROM PUBLIC;

-- Grant hook (api.check_role_assignment_grant) with the delegation check, for membership changes that hand
-- a group's assignments to a new member.
CREATE OR REPLACE FUNCTION api.check_role_assignment_grant(
    p_actor UUID,
    p_spec api.role_assignment_spec
)
RETURNS BOOLEAN AS $$
BEGIN
    IF api.has_permission_on_role_assignment(p_actor, 'role_assignment:create', p_spec) IS NOT TRUE THEN
        RETURN FALSE;
    END IF;

    IF api.has_permission(p_actor, 'role_assignment:create', NULL) IS TRUE THEN
        RETURN TRUE;
    END IF;

    BEGIN
        PERFORM api.check_role_assignment_delegation(p_actor, 'role_assignment:create', p_spec);
    EXCEPTION WHEN sqlstate 'PGRST' THEN
        RETURN FALSE;
    END;

    RETURN TRUE;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

COMMENT ON FUNCTION api.check_role_assignment_grant(UUID, api.role_assignment_spec) IS
    'Extension point: may p_actor create an assignment like p_spec? Default: the RLS create rule plus the delegation check.';

CREATE OR REPLACE FUNCTION api.role_assignments_check_delegated_grant()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_actor UUID := auth.uid();
    v_action api.permission_action;
BEGIN
    IF v_actor IS NOT NULL THEN
        IF TG_OP = 'INSERT' THEN
            v_action := 'role_assignment:create';
        ELSIF TG_OP = 'DELETE' THEN
            v_action := 'role_assignment:delete';
        ELSIF (OLD.metadata).deletion_timestamp IS NULL AND (NEW.metadata).deletion_timestamp IS NOT NULL THEN
            v_action := 'role_assignment:delete';
        ELSE
            v_action := 'role_assignment:update';
        END IF;

        IF api.has_permission(v_actor, v_action, NULL) IS NOT TRUE THEN
            IF TG_OP IN ('UPDATE', 'DELETE') THEN
                PERFORM api.check_role_assignment_delegation(v_actor, v_action, OLD.spec);
            END IF;

            IF TG_OP IN ('INSERT', 'UPDATE') THEN
                PERFORM api.check_role_assignment_delegation(v_actor, v_action, NEW.spec);
            END IF;
        END IF;
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;

    RETURN NEW;
END;
$$;

REVOKE ALL ON FUNCTION api.role_assignments_check_delegated_grant() FROM PUBLIC;

-- The name sorts after validate_role_assignments_on_role_assignments, so the
-- check sees the normalized scope.
CREATE TRIGGER validate_role_delegated_grant_on_role_assignments
    BEFORE INSERT OR UPDATE OR DELETE ON api.role_assignments
    FOR EACH ROW
    EXECUTE FUNCTION api.role_assignments_check_delegated_grant();

-- team:assign-member is checked on the Team.
CREATE OR REPLACE FUNCTION api.team_members_check_grants()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_actor UUID := auth.uid();
    v_ra RECORD;
BEGIN
    IF v_actor IS NULL
        OR api.has_permission_on_team(v_actor, 'team:assign-member', NEW.team_id) IS NOT TRUE THEN
        RETURN NEW;
    END IF;

    FOR v_ra IN
        SELECT ra.metadata, ra.spec
        FROM api.teams t
        JOIN api.role_assignments ra
          ON (ra.spec).subject_name = (t.metadata).name
         AND (ra.spec).subject_kind = 'Team'
        WHERE t.id = NEW.team_id
          AND (ra.metadata).deletion_timestamp IS NULL
    LOOP
        IF api.check_role_assignment_grant(v_actor, v_ra.spec) IS NOT TRUE THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10296","message": "Adding the member grants role assignment ''%s'', which you cannot create","hint": "Ask someone who can create that role assignment"}',
                    (v_ra.metadata).name),
                detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END LOOP;

    RETURN NEW;
END;
$$;

CREATE TRIGGER team_members_check_grants
    BEFORE INSERT ON api.team_members
    FOR EACH ROW
    EXECUTE FUNCTION api.team_members_check_grants();

REVOKE ALL ON FUNCTION api.team_members_check_grants() FROM PUBLIC;

-- A move only checks the OrgUnits on the new path that were not on the old
-- one, so an assignment on a shared ancestor does not block a move inside its
-- subtree. Inactive OrgUnits are checked too: sync may make them Active again.
CREATE OR REPLACE FUNCTION api.org_unit_members_check_grants()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_actor UUID := auth.uid();
    v_old_ids INTEGER[] := ARRAY[]::INTEGER[];
    v_ra RECORD;
BEGIN
    IF v_actor IS NULL OR NEW.identity_source IS NOT NULL THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'INSERT' THEN
        IF api.has_permission(v_actor, 'org_unit:assign-member', NULL) IS NOT TRUE THEN
            RETURN NEW;
        END IF;
    ELSIF OLD.identity_source IS NOT NULL
        OR api.has_permission_on_org_unit(v_actor, 'org_unit:assign-member', OLD.org_unit_id) IS NOT TRUE
        OR api.has_permission_on_org_unit(v_actor, 'org_unit:assign-member', NEW.org_unit_id) IS NOT TRUE THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' AND NEW.user_id = OLD.user_id THEN
        IF NEW.org_unit_id = OLD.org_unit_id THEN
            RETURN NEW;
        END IF;

        SELECT api.org_unit_ancestor_ids(u.path) INTO v_old_ids
        FROM api.org_units u
        WHERE u.id = OLD.org_unit_id;
    END IF;

    FOR v_ra IN
        SELECT ra.metadata, ra.spec
        FROM api.org_units u
        JOIN api.org_units s ON s.id = ANY (api.org_unit_ancestor_ids(u.path))
        JOIN api.role_assignments ra
          ON (ra.spec).subject_name = (s.metadata).name
         AND (ra.spec).subject_kind = 'OrgUnit'
        WHERE u.id = NEW.org_unit_id
          AND NOT (s.id = ANY (COALESCE(v_old_ids, ARRAY[]::INTEGER[])))
          AND (ra.metadata).deletion_timestamp IS NULL
    LOOP
        IF api.check_role_assignment_grant(v_actor, v_ra.spec) IS NOT TRUE THEN
            RAISE sqlstate 'PGRST'
                USING message = format('{"code": "10296","message": "Adding the member grants role assignment ''%s'', which you cannot create","hint": "Ask someone who can create that role assignment"}',
                    (v_ra.metadata).name),
                detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
        END IF;
    END LOOP;

    RETURN NEW;
END;
$$;

CREATE TRIGGER org_unit_members_check_grants
    BEFORE INSERT OR UPDATE ON api.org_unit_members
    FOR EACH ROW
    EXECUTE FUNCTION api.org_unit_members_check_grants();

REVOKE ALL ON FUNCTION api.org_unit_members_check_grants() FROM PUBLIC;

-- A Team with ServiceAccounts is not deleted (10315).
CREATE OR REPLACE FUNCTION api.teams_before_delete()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
DECLARE
    v_count INTEGER;
    v_accounts INTEGER;
BEGIN
    IF auth.uid() IS NOT NULL AND (OLD.spec).identity_source IS NOT NULL THEN
        RAISE sqlstate 'PGRST'
            USING message = '{"code": "10289","message": "A Team synced from an identity source is read-only","hint": "Sync sets it Inactive when the group is gone from the directory"}',
            detail = '{"status": 403, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    SELECT count(*) INTO v_count
    FROM api.role_assignments ra
    WHERE (((ra.spec).subject_kind = 'Team' AND (ra.spec).subject_name = (OLD.metadata).name)
           OR (ra.spec).team = (OLD.metadata).name)
      AND (ra.metadata).deletion_timestamp IS NULL;

    IF v_count > 0 THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10295","message": "cannot delete Team ''%s''","hint": "%s role assignment(s) still reference this Team"}',
                (OLD.metadata).name, v_count),
            detail = '{"status": 409, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    SELECT count(*) INTO v_accounts FROM api.service_accounts s WHERE s.team_id = OLD.id;

    IF v_accounts > 0 THEN
        RAISE sqlstate 'PGRST'
            USING message = format('{"code": "10315","message": "cannot delete Team ''%s''","hint": "%s ServiceAccount(s) of this Team still exist; delete them first"}',
                (OLD.metadata).name, v_accounts),
            detail = '{"status": 409, "headers": {"X-Powered-By": "Neutree"}}';
    END IF;

    RETURN OLD;
END;
$$;

-- team:assign-member is checked on the Team.
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
        AND api.has_permission_on_team(auth.uid(), 'team:assign-member', v_team_id) THEN
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

-- ----------------------
-- api.get_user_effective_role_assignments
-- ----------------------
-- The role assignments that apply to a user, with where each comes from:
-- via_kind User (the user's own), Team, or OrgUnit (an OrgUnit on the path to
-- the user's primary OrgUnit), and via_name the group's metadata.name.
--
-- The caller needs role_assignment:read on the user (global, or a subtree
-- that holds the user); otherwise 42501 (HTTP 403). It then sees every
-- assignment that applies to the user, group ones included, even where the
-- role_assignments RLS would hide the group's assignment itself.
CREATE OR REPLACE FUNCTION api.get_user_effective_role_assignments(p_user_id UUID)
RETURNS TABLE (
    id INTEGER,
    name TEXT,
    spec api.role_assignment_spec,
    via_kind TEXT,
    via_name TEXT
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog
AS $$
BEGIN
    IF api.has_permission_on_subject(auth.uid(), 'role_assignment:read', p_user_id) IS NOT TRUE THEN
        RAISE insufficient_privilege
            USING message = 'role_assignment:read on this user is required';
    END IF;

    RETURN QUERY
    SELECT ra.id,
           (ra.metadata).name,
           ra.spec,
           COALESCE((ra.spec).subject_kind, 'User'),
           (ra.spec).subject_name
    FROM api.user_role_assignments(p_user_id) ra
    ORDER BY 4, 5, 2;
END;
$$;

-- ----------------------
-- team-admin and model-invoker
-- ----------------------
-- team-admin is meant to be assigned with spec.team. On its Team it manages
-- the Team's ServiceAccounts and their API keys (service_account:*), reads
-- the Team's usage and token pool (team_usage:read), reads the Team and adds
-- or removes members of a Team created in Neutree. It holds nothing that
-- changes group policies, token pools or application permissions, grants
-- roles or creates users. Assigned globally it would act on every Team.
-- model-invoker calls models in the workspaces it covers.

CREATE OR REPLACE FUNCTION api.update_team_admin_permissions()
RETURNS VOID AS $$
BEGIN
    UPDATE api.roles
    SET spec = ROW((spec).preset_key, ARRAY[
        'service_account:read',
        'service_account:create',
        'service_account:update',
        'service_account:delete',
        'team_usage:read',
        'team:read',
        'team:assign-member'
    ]::api.permission_action[])::api.role_spec
    WHERE (metadata).name = 'team-admin';
END;
$$ LANGUAGE plpgsql;

INSERT INTO api.roles (api_version, kind, metadata, spec)
SELECT 'v1', 'Role',
    ROW('team-admin', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
    ROW('team-admin'::api.role_preset, ARRAY[]::api.permission_action[])::api.role_spec
WHERE NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = 'team-admin');

INSERT INTO api.roles (api_version, kind, metadata, spec)
SELECT 'v1', 'Role',
    ROW('model-invoker', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
    ROW('model-invoker'::api.role_preset, ARRAY['model:invoke', 'workspace:read']::api.permission_action[])::api.role_spec
WHERE NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = 'model-invoker');

SELECT api.update_team_admin_permissions();

SELECT api.update_admin_permissions();

-- ----------------------
-- Teams, members and ServiceAccounts, checked on the Team
-- ----------------------
-- A synced Team stays read-only (10290), and adding a member still needs the
-- right to create every role assignment of the Team (10296).

DROP POLICY IF EXISTS "team read policy" ON api.teams;

DROP POLICY IF EXISTS "team_member read policy" ON api.team_members;

DROP POLICY IF EXISTS "team_member create policy" ON api.team_members;

DROP POLICY IF EXISTS "team_member delete policy" ON api.team_members;

CREATE POLICY "team read policy" ON api.teams
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'team:read', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'team:read', id)
    );

CREATE POLICY "team_member read policy" ON api.team_members
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'team:read', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'team:read', team_id)
    );

CREATE POLICY "team_member create policy" ON api.team_members
    FOR INSERT
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'team:assign-member', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'team:assign-member', team_id)
    );

CREATE POLICY "team_member delete policy" ON api.team_members
    FOR DELETE
    USING (
        (SELECT api.has_permission(auth.uid(), 'team:assign-member', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'team:assign-member', team_id)
    );

ALTER TABLE api.service_accounts ENABLE ROW LEVEL SECURITY;

CREATE POLICY "service_account read policy" ON api.service_accounts
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'service_account:read', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'service_account:read', team_id)
    );

CREATE POLICY "service_account create policy" ON api.service_accounts
    FOR INSERT
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'service_account:create', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'service_account:create', team_id)
    );

CREATE POLICY "service_account update policy" ON api.service_accounts
    FOR UPDATE
    USING (
        (SELECT api.has_permission(auth.uid(), 'service_account:update', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'service_account:update', team_id)
    )
    WITH CHECK (
        (SELECT api.has_permission(auth.uid(), 'service_account:update', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'service_account:update', team_id)
    );

CREATE POLICY "service_account delete policy" ON api.service_accounts
    FOR DELETE
    USING (
        (SELECT api.has_permission(auth.uid(), 'service_account:delete', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'service_account:delete', team_id)
    );

ALTER TABLE api.team_application_permissions ENABLE ROW LEVEL SECURITY;

CREATE POLICY "team_application_permission read policy" ON api.team_application_permissions
    FOR SELECT
    USING (
        (SELECT api.has_permission(auth.uid(), 'role_assignment:read', NULL))
        OR (SELECT api.has_permission(auth.uid(), 'service_account:read', NULL))
        OR api.has_team_scoped_permission(auth.uid(), 'service_account:read', team_id)
    );

CREATE POLICY "team_application_permission create policy" ON api.team_application_permissions
    FOR INSERT
    WITH CHECK ((SELECT api.has_permission(auth.uid(), 'role_assignment:create', NULL)));

CREATE POLICY "team_application_permission update policy" ON api.team_application_permissions
    FOR UPDATE
    USING ((SELECT api.has_permission(auth.uid(), 'role_assignment:create', NULL)))
    WITH CHECK ((SELECT api.has_permission(auth.uid(), 'role_assignment:create', NULL)));

CREATE POLICY "team_application_permission delete policy" ON api.team_application_permissions
    FOR DELETE
    USING ((SELECT api.has_permission(auth.uid(), 'role_assignment:create', NULL)));

-- ----------------------
-- Profiles
-- ----------------------
-- A profile (name, display name, email) is readable also:
--   * when the user is a member of a Team the caller reads (team_members
--     RLS keeps the subquery to those Teams);
--   * by whoever may add members to some Team, to pick users from.

DROP POLICY IF EXISTS "user_profile read policy" ON api.user_profiles;

CREATE POLICY "user_profile read policy" ON api.user_profiles
    FOR SELECT
    USING (
        id = auth.uid()
        OR (SELECT api.has_permission_in_any_scope(auth.uid(), 'team:assign-member'))
        OR id IN (SELECT tm.user_id FROM api.team_members tm)
        OR (SELECT api.has_permission(auth.uid(), 'user_profile:read', NULL))
        OR api.has_org_scoped_permission_on_subject(auth.uid(), 'user_profile:read', id)
    );
