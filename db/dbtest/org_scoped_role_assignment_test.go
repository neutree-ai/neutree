package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// Tests for 112_org_scoped_role_assignments in the community edition:
//   - writing an OrgUnit scope is rejected and never turned into a global
//     assignment,
//   - api.has_permission_on_subject / api.has_permission_on_org_unit follow
//     global assignments only (the subtree hook never grants),
//   - the department-admin preset role exists and is protected.

func TestOrgScopedRoleAssignment_EntryPointsExist(t *testing.T) {
	db := GetTestDB(t)

	cases := []struct {
		name string
		args string
	}{
		{"check_org_role_binding", "user_uuid uuid, required_permission api.permission_action, org_unit_id integer"},
		{"has_permission_on_subject", "user_uuid uuid, required_permission api.permission_action, target_user uuid"},
		{"has_permission_on_org_unit", "user_uuid uuid, required_permission api.permission_action, org_unit_id integer"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var args string
			var secDef bool
			err := db.QueryRow(`
				SELECT pg_get_function_identity_arguments(p.oid), p.prosecdef
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'api' AND p.proname = $1
			`, c.name).Scan(&args, &secDef)
			if err != nil {
				t.Fatalf("function api.%s not found: %v", c.name, err)
			}
			if args != c.args {
				t.Errorf("api.%s arguments = %q, want %q", c.name, args, c.args)
			}
			if !secDef {
				t.Errorf("api.%s must be SECURITY DEFINER", c.name)
			}
		})
	}
}

func TestOrgScopedRoleAssignment_CommunityRejectsScope(t *testing.T) {
	f := newOrgFixture(t)
	unit := f.createOrgUnit("")
	user := createGoTrueTestUser(t, "osra-reject", "password123")
	db := GetTestDB(t)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id = $1::uuid`, user.ID)
	})

	for _, global := range []string{"FALSE", "TRUE", "NULL"} {
		t.Run("global="+global, func(t *testing.T) {
			_, err := db.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment',
					ROW($1, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
					ROW($2::uuid, NULL, `+global+`, 'workspace-user', $3)::api.role_assignment_spec)
			`, newOrgName("osra"), user.ID, unit.Metadata.Name)
			assertErrCode(t, "an OrgUnit-scoped assignment", err, "10280")
		})
	}

	t.Run("an update cannot add a scope", func(t *testing.T) {
		name := newOrgName("osra")
		if _, err := db.Exec(`
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment',
				ROW($1, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
				ROW($2::uuid, NULL, TRUE, 'workspace-user', NULL)::api.role_assignment_spec)
		`, name, user.ID); err != nil {
			t.Fatalf("global assignment failed: %v", err)
		}

		_, err := db.Exec(`
			UPDATE api.role_assignments
			SET spec = ROW((spec).user_id, NULL, FALSE, (spec).role, $2)::api.role_assignment_spec
			WHERE (metadata).name = $1
		`, name, unit.Metadata.Name)
		assertErrCode(t, "adding an OrgUnit scope", err, "10280")
	})

	var scoped int
	if err := db.QueryRow(`SELECT count(*) FROM api.role_assignments WHERE (spec).user_id = $1::uuid AND ((spec).org_unit IS NOT NULL OR (spec).role <> 'workspace-user')`,
		user.ID).Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if scoped != 0 {
		t.Fatalf("%d scoped assignments were stored", scoped)
	}

	var globals int
	if err := db.QueryRow(`SELECT count(*) FROM api.role_assignments WHERE (spec).user_id = $1::uuid`, user.ID).Scan(&globals); err != nil {
		t.Fatal(err)
	}
	if globals != 1 {
		t.Fatalf("user has %d assignments, want only the one global assignment", globals)
	}
}

func TestOrgScopedRoleAssignment_CommunityNeverGrantsBySubtree(t *testing.T) {
	ctx := context.Background()
	f := newOrgFixture(t)
	root := f.createOrgUnit("")
	child := f.createOrgUnit(root.Metadata.Name)
	db := GetTestDB(t)

	target := createGoTrueTestUser(t, "osra-target", "password123")
	if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: target.ID, OrgUnitID: child.ID, IdentitySource: f.source}); err != nil {
		t.Fatalf("SetOrgUnitMember failed: %v", err)
	}

	var globalID string
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		globalID = createUserWithPermissions(t, tx, "osra-global", "osra-global@example.com",
			[]string{"user_profile:read", "org_unit:read", "role:read"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A subtree-scoped department-admin assignment, as the enterprise edition
	// would store it. Community rejects writing one, so the write checks are
	// bypassed for this seed only.
	scoped := createGoTrueTestUser(t, "osra-scoped", "password123")
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment',
				ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW($2::uuid, NULL, FALSE, 'department-admin', $3)::api.role_assignment_spec)
		`, newOrgName("osra-scoped"), scoped.ID, root.Metadata.Name)
		return err
	}); err != nil {
		t.Fatalf("seeding the scoped assignment failed: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id IN ($1::uuid, $2::uuid)`, globalID, scoped.ID)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = 'osra-global-role'`)
	})

	check := func(t *testing.T, query string, args []any, want bool) {
		t.Helper()

		var got bool
		if err := db.QueryRow(query, args...).Scan(&got); err != nil {
			t.Fatalf("%s failed: %v", query, err)
		}
		if got != want {
			t.Fatalf("%s %v = %v, want %v", query, args, got, want)
		}
	}

	onSubject := `SELECT api.has_permission_on_subject($1::uuid, $2::api.permission_action, $3::uuid)`
	onOrgUnit := `SELECT api.has_permission_on_org_unit($1::uuid, $2::api.permission_action, $3)`

	t.Run("global assignment grants on any subject", func(t *testing.T) {
		check(t, onSubject, []any{globalID, "user_profile:read", target.ID}, true)
		check(t, onSubject, []any{globalID, "user_profile:read", scoped.ID}, true)
		check(t, onSubject, []any{globalID, "user_profile:update", target.ID}, false)
		check(t, onOrgUnit, []any{globalID, "org_unit:read", child.ID}, true)
		check(t, onOrgUnit, []any{globalID, "role:read", nil}, true)
	})

	t.Run("subtree assignment grants nothing", func(t *testing.T) {
		check(t, `SELECT api.check_org_role_binding($1::uuid, 'user_profile:read', $2)`, []any{scoped.ID, child.ID}, false)
		check(t, onSubject, []any{scoped.ID, "user_profile:read", target.ID}, false)
		check(t, onSubject, []any{scoped.ID, "role_assignment:create", target.ID}, false)
		check(t, onOrgUnit, []any{scoped.ID, "org_unit:read", root.ID}, false)
		check(t, onOrgUnit, []any{scoped.ID, "role:read", nil}, false)
		check(t, `SELECT api.has_permission($1::uuid, 'user_profile:read', NULL)`, []any{scoped.ID}, false)
	})

	t.Run("RLS follows global assignments only", func(t *testing.T) {
		count := func(userID, query string, args ...any) int {
			t.Helper()

			var n int
			if err := executeAsUser(t, db, userID, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, query, args...).Scan(&n)
			}); err != nil {
				t.Fatalf("%s failed: %v", query, err)
			}

			return n
		}

		if n := count(scoped.ID, `SELECT count(*) FROM api.user_profiles WHERE id = $1::uuid`, target.ID); n != 0 {
			t.Errorf("subtree assignment sees the target profile")
		}
		if n := count(scoped.ID, `SELECT count(*) FROM api.org_units WHERE id = $1`, child.ID); n != 0 {
			t.Errorf("subtree assignment sees the OrgUnit")
		}
		if n := count(scoped.ID, `SELECT count(*) FROM api.roles`); n != 0 {
			t.Errorf("subtree assignment sees roles")
		}
		if n := count(globalID, `SELECT count(*) FROM api.user_profiles WHERE id = $1::uuid`, target.ID); n != 1 {
			t.Errorf("global assignment does not see the target profile")
		}
	})
}

func TestOrgScopedRoleAssignment_DepartmentAdminPreset(t *testing.T) {
	db := GetTestDB(t)

	var preset, perms string
	if err := db.QueryRow(`SELECT (spec).preset_key::text, (spec).permissions::text FROM api.roles WHERE (metadata).name = 'department-admin'`).
		Scan(&preset, &perms); err != nil {
		t.Fatalf("department-admin role not found: %v", err)
	}

	if preset != "department-admin" {
		t.Errorf("preset_key = %q, want department-admin", preset)
	}

	want := []string{
		"user_profile:read", "user_profile:update",
		"role_assignment:read", "role_assignment:create", "role_assignment:update", "role_assignment:delete",
		"role:read", "org_unit:read",
	}
	got := strings.Split(strings.Trim(perms, "{}"), ",")
	if len(got) != len(want) {
		t.Fatalf("department-admin permissions = %s, want %v", perms, want)
	}
	for i := range want {
		if strings.Trim(got[i], `"`) != want[i] {
			t.Fatalf("department-admin permissions = %s, want %v", perms, want)
		}
	}

	var userID string
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		userID = createUserWithPermissions(t, tx, "osra-role-editor", "osra-role-editor@example.com",
			[]string{"role:read", "role:update", "role:delete"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id = $1::uuid`, userID)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = 'osra-role-editor-role'`)
	})

	err := executeAsUser(t, db, userID, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			UPDATE api.roles
			SET spec = ROW((spec).preset_key, ARRAY['workspace:read']::api.permission_action[])::api.role_spec
			WHERE (metadata).name = 'department-admin'`)
		return err
	})
	if err == nil {
		t.Fatal("updating the department-admin preset role succeeded")
	}

	err = executeAsUser(t, db, userID, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM api.roles WHERE (metadata).name = 'department-admin'`)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 0 {
			t.Fatalf("deleting the department-admin preset role removed %d rows", n)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}
