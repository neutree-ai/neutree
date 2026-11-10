package dbtest

import (
	"database/sql"
	"strings"
	"testing"
)

// Tests for the roles of ServiceAccounts: an SA holds its Team's application
// permissions (team_application_permissions), never role assignments of its
// own, and never the roles of its Team.

// insertSAAssignment inserts a global assignment of role to the SA with the
// given subject kind, as postgres.
func insertSAAssignment(t *testing.T, db *sql.DB, saID, role, kind string) error {
	t.Helper()

	name, err := insertRoleAssignment(db, roleAssignment{UserID: saID, Global: true, Role: role, SubjectKind: kind})

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (metadata).name = $1`, name)
	})

	return err
}

// setAppPermissions stores the application permissions of teamID as postgres; nil
// workspaces is every workspace. The row is removed when the test ends.
func setAppPermissions(t *testing.T, db *sql.DB, teamID int, roles []string, workspaces []string) {
	t.Helper()

	var ws any
	if workspaces != nil {
		ws = "{" + strings.Join(workspaces, ",") + "}"
	}

	// Replica mode skips the check that the workspaces exist: only one can.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
	mustTxExec(t, tx, `
		INSERT INTO api.team_application_permissions (team_id, roles, workspaces)
		VALUES ($1, $2::text[], $3::text[])
		ON CONFLICT (team_id) DO UPDATE SET roles = EXCLUDED.roles, workspaces = EXCLUDED.workspaces`,
		teamID, "{"+strings.Join(roles, ",")+"}", ws)

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.team_application_permissions WHERE team_id = $1`, teamID)
	})
}

func TestServiceAccountRBAC_DefaultPermissions(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)

	if !hasPermission(t, sa, "model:invoke", saWorkspace) {
		t.Fatal("a new SA may not call models under the default permissions")
	}

	if hasPermission(t, sa, "endpoint:read", saWorkspace) {
		t.Fatal("a new SA holds more than model-invoker")
	}

	var kind, name, role string
	var global bool
	if err := f.db.QueryRow(`
		SELECT (spec).subject_kind, (spec).subject_name, (spec).role, (spec).global
		FROM api.user_role_assignments($1)`, sa).Scan(&kind, &name, &role, &global); err != nil {
		t.Fatal(err)
	}

	if kind != "Team" || name != f.team || role != "model-invoker" || !global {
		t.Fatalf("the SA's assignment: %s %s %s global=%v; want Team %s model-invoker global", kind, name, role, global, f.team)
	}
}

func TestServiceAccountRBAC_TeamPermissionsApplyAtOnce(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	reader := customRole(t, f.db, "endpoint:read")

	setAppPermissions(t, f.db, f.teamID, []string{"model-invoker", reader}, nil)

	if !hasPermission(t, sa, "endpoint:read", saWorkspace) || !hasPermission(t, sa, "model:invoke", saWorkspace) {
		t.Fatal("the SA lacks a role of its Team's application permissions")
	}

	setAppPermissions(t, f.db, f.teamID, []string{reader}, nil)

	if hasPermission(t, sa, "model:invoke", saWorkspace) {
		t.Error("model-invoker taken out still counts")
	}

	if !hasPermission(t, sa, "endpoint:read", saWorkspace) {
		t.Error("a role left in stopped counting")
	}

	setAppPermissions(t, f.db, f.teamID, []string{"model-invoker"}, []string{"sa-other-ws"})

	if hasPermission(t, sa, "model:invoke", saWorkspace) {
		t.Error("permissions limited to another workspace count here")
	}

	mustExec(t, f.db, `DELETE FROM api.team_application_permissions WHERE team_id = $1`, f.teamID)

	if !hasPermission(t, sa, "model:invoke", saWorkspace) || hasPermission(t, sa, "endpoint:read", saWorkspace) {
		t.Error("the default is not model-invoker only")
	}
}

func TestServiceAccountRBAC_NoRoleAssignments(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)

	err := commitAs(t, f.db, f.admin, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW($2::uuid, NULL, TRUE, 'model-invoker', NULL, 'ServiceAccount', NULL, NULL)::api.role_assignment_spec)`,
			newOrgName("rasa"), sa)

		return err
	})
	wantErr(t, "team-admin assigning a role to the SA", err, "10326")

	wantErr(t, "postgres assigning a role to the SA", insertSAAssignment(t, f.db, sa, "model-invoker", "ServiceAccount"), "10326")
	wantErr(t, "the SA assigned as a user", insertSAAssignment(t, f.db, sa, "model-invoker", "User"), "10326")

	var exists bool
	if err := f.db.QueryRow(`
		SELECT EXISTS (
			SELECT 1 FROM pg_enum e JOIN pg_type ty ON ty.oid = e.enumtypid
			WHERE ty.typname = 'permission_action' AND e.enumlabel = 'service_account:assign-role')`).Scan(&exists); err != nil || exists {
		t.Fatalf("a permission grants roles to ServiceAccounts: %v, %v", exists, err)
	}
}

func TestServiceAccountRBAC_NotTheTeamsRoles(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	role := customRole(t, f.db, "model:invoke", "endpoint:read")
	assignGroup(t, "Team", f.team, role)

	if !hasPermission(t, f.member, "endpoint:read", saWorkspace) {
		t.Fatal("a member lacks the Team's role")
	}

	if hasPermission(t, sa, "endpoint:read", saWorkspace) {
		t.Fatal("the SA holds the Team's role")
	}

	var inTeam bool
	if err := f.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM api.user_groups($1) WHERE kind = 'Team' AND name = $2)`, sa, f.team).Scan(&inTeam); err != nil || !inTeam {
		t.Fatalf("the SA's groups lack its Team: %v, %v", inTeam, err)
	}
}

func TestServiceAccountRBAC_SuspendedSA(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)

	check := func(tx *sql.Tx) bool {
		var ok bool
		if err := tx.QueryRow(`SELECT api.has_permission($1, 'model:invoke', $2)`, sa, saWorkspace).Scan(&ok); err != nil {
			t.Fatal(err)
		}

		return ok
	}

	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if !check(tx) {
		t.Fatal("an enabled SA lacks the default permissions")
	}

	mustTxExec(t, tx, `SAVEPOINT s`)
	mustTxExec(t, tx, `UPDATE api.service_accounts SET spec.disabled = TRUE WHERE id = $1`, sa)

	if check(tx) {
		t.Error("a disabled SA keeps its roles")
	}

	mustTxExec(t, tx, `ROLLBACK TO SAVEPOINT s`)
	mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
	mustTxExec(t, tx, `UPDATE api.teams SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, f.teamID)

	if check(tx) {
		t.Error("an SA of an Inactive Team keeps its roles")
	}
}

func TestServiceAccountRBAC_AppPermissionsWrite(t *testing.T) {
	f := newSAFixture(t)

	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM api.team_application_permissions WHERE team_id = $1`, f.teamID)
	})

	read := func(user string) (string, error) {
		var roles string

		err := asUser(t, f.db, user, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT roles::text || ':' || is_default FROM api.get_team_application_permissions($1)`, f.teamID).Scan(&roles)
		})

		return roles, err
	}

	if got, err := read(f.admin); err != nil || got != "{model-invoker}:true" {
		t.Fatalf("team-admin reads the default permissions: %s, %v", got, err)
	}

	_, err := read(f.member)
	wantErr(t, "a member reading the permissions", err, "is required to read its application permissions")

	err = commitAs(t, f.db, f.admin, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO api.team_application_permissions (team_id, roles) VALUES ($1, '{workspace-user}')`, f.teamID)
		return err
	})
	wantErr(t, "team-admin raising its own Team's permissions", err, "row-level security")

	_, err = f.db.Exec(`INSERT INTO api.team_application_permissions (team_id, roles) VALUES ($1, '{sa-no-such-role}')`, f.teamID)
	wantErr(t, "an unknown role", err, "10324")

	_, err = f.db.Exec(`INSERT INTO api.team_application_permissions (team_id, roles, workspaces) VALUES ($1, '{model-invoker}', '{sa-no-such-ws}')`, f.teamID)
	wantErr(t, "an unknown workspace", err, "10324")
}

func TestServiceAccountRBAC_AppPermissionsHoldNoManagementRole(t *testing.T) {
	f := newSAFixture(t)

	t.Cleanup(func() {
		_, _ = f.db.Exec(`DELETE FROM api.team_application_permissions WHERE team_id = $1`, f.teamID)
	})

	write := func(roles string) error {
		_, err := f.db.Exec(`
			INSERT INTO api.team_application_permissions (team_id, roles) VALUES ($1, $2::text[])
			ON CONFLICT (team_id) DO UPDATE SET roles = EXCLUDED.roles`, f.teamID, roles)
		return err
	}

	granter := customRole(t, f.db, "endpoint:read", "role_assignment:create")
	for _, role := range []string{"admin", "team-admin", "department-admin", granter} {
		wantErr(t, "application permissions with "+role, write("{model-invoker,"+role+"}"), "10325")
	}

	reader := customRole(t, f.db, "endpoint:read")
	if err := write("{model-invoker,workspace-user," + reader + "}"); err != nil {
		t.Fatalf("application permissions of roles that manage nothing: %v", err)
	}

	t.Run("a role edited into management stops counting", func(t *testing.T) {
		sa := f.mustSA(t)

		if err := write("{model-invoker," + reader + "}"); err != nil {
			t.Fatal(err)
		}

		if !hasPermission(t, sa, "endpoint:read", saWorkspace) {
			t.Fatal("the SA lacks a role of its Team's application permissions")
		}

		mustExec(t, f.db, `UPDATE api.roles SET spec = ROW(NULL, '{endpoint:read,user_profile:update}')::api.role_spec WHERE (metadata).name = $1`, reader)

		if hasPermission(t, sa, "user_profile:update", "") || hasPermission(t, sa, "endpoint:read", saWorkspace) {
			t.Error("a role that now manages users still counts")
		}

		var roles string
		if err := f.db.QueryRow(`SELECT roles::text FROM api.effective_team_application_permissions($1)`, f.teamID).Scan(&roles); err != nil {
			t.Fatal(err)
		}

		if strings.Contains(roles, reader) {
			t.Errorf("the permissions still list %s: %s", reader, roles)
		}
	})
}
