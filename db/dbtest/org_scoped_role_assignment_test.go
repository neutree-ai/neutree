package dbtest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// Tests for OrgUnit-scoped role assignments and their bindings:
//   - an OrgUnit scope is stored as is; a workspace scope is still rejected,
//   - api.has_permission_on_subject / api.has_permission_on_org_unit follow
//     global assignments and the subtree of OrgUnit-scoped ones,
//   - the department-admin preset role exists and is protected.
// Delegation and Inactive OrgUnits: org_subtree_*_test.go.

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

func TestOrgScopedRoleAssignment_Scopes(t *testing.T) {
	f := newOrgFixture(t)
	unit := f.createOrgUnit("")
	user := createGoTrueTestUser(t, "osra-scope", "password123")
	db := GetTestDB(t)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id = $1::uuid`, user.ID)
	})

	insert := func(tx *sql.Tx, workspace, global, orgUnit any) (string, error) {
		name := newOrgName("osra")
		_, err := tx.Exec(`
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment',
				ROW($1, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
				ROW($2::uuid, $3, $4, 'workspace-user', $5, NULL, NULL, NULL)::api.role_assignment_spec)
		`, name, user.ID, workspace, global, orgUnit)
		return name, err
	}

	inTx := func(t *testing.T, fn func(tx *sql.Tx)) {
		t.Helper()

		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		fn(tx)
	}

	scopeOf := func(t *testing.T, tx *sql.Tx, name string) (bool, string) {
		t.Helper()

		var global sql.NullBool
		var ou string
		if err := tx.QueryRow(`SELECT (spec).global, COALESCE((spec).org_unit, '') FROM api.role_assignments WHERE (metadata).name = $1`,
			name).Scan(&global, &ou); err != nil {
			t.Fatal(err)
		}
		return global.Bool, ou
	}

	for _, global := range []any{false, nil} {
		t.Run(fmt.Sprintf("an OrgUnit scope with global=%v is stored", global), func(t *testing.T) {
			inTx(t, func(tx *sql.Tx) {
				name, err := insert(tx, nil, global, unit.Metadata.Name)
				if err != nil {
					t.Fatalf("insert failed: %v", err)
				}
				if g, ou := scopeOf(t, tx, name); g || ou != unit.Metadata.Name {
					t.Errorf("stored global=%v org_unit=%q", g, ou)
				}
			})
		})
	}

	t.Run("an OrgUnit scope cannot be global", func(t *testing.T) {
		inTx(t, func(tx *sql.Tx) {
			_, err := insert(tx, nil, true, unit.Metadata.Name)
			assertErrCode(t, "global with an OrgUnit", err, "10281")
		})
	})

	t.Run("a workspace scope is still rejected", func(t *testing.T) {
		inTx(t, func(tx *sql.Tx) {
			_, err := insert(tx, "default", false, nil)
			assertErrCode(t, "a workspace scope", err, "10041")
		})
	})

	t.Run("a soft-deleted OrgUnit-scoped assignment does not block creating it again", func(t *testing.T) {
		inTx(t, func(tx *sql.Tx) {
			if _, err := insert(tx, nil, false, unit.Metadata.Name); err != nil {
				t.Fatal(err)
			}
			if _, err := insert(tx, nil, false, unit.Metadata.Name); err == nil {
				t.Fatal("a live duplicate was accepted")
			}
		})

		inTx(t, func(tx *sql.Tx) {
			name, err := insert(tx, nil, false, unit.Metadata.Name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE api.role_assignments SET metadata.deletion_timestamp = now() WHERE (metadata).name = $1`, name); err != nil {
				t.Fatal(err)
			}
			if _, err := insert(tx, nil, false, unit.Metadata.Name); err != nil {
				t.Errorf("creating it again after a soft delete failed: %v", err)
			}
		})
	})

	// PostgREST writes spec as a whole; a role-only PATCH must not widen an
	// OrgUnit scope to global.
	t.Run("a role-only update keeps the stored scope", func(t *testing.T) {
		for _, c := range []struct {
			global  any
			orgUnit any
		}{{false, unit.Metadata.Name}, {true, nil}} {
			inTx(t, func(tx *sql.Tx) {
				name, err := insert(tx, nil, c.global, c.orgUnit)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`UPDATE api.role_assignments SET spec = ROW(NULL, NULL, NULL, 'admin', NULL, NULL, NULL, NULL)::api.role_assignment_spec
					WHERE (metadata).name = $1`, name); err != nil {
					t.Fatalf("role-only update failed: %v", err)
				}
				wantOU, _ := c.orgUnit.(string)
				if g, ou := scopeOf(t, tx, name); g != (c.global == true) || ou != wantOU {
					t.Errorf("after a role-only update of global=%v org_unit=%v: global=%v org_unit=%q", c.global, c.orgUnit, g, ou)
				}
			})
		}
	})
}

func TestOrgScopedRoleAssignment_GrantsBySubtree(t *testing.T) {
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

	// A subtree-scoped department-admin assignment.
	scoped := createGoTrueTestUser(t, "osra-scoped", "password123")
	seedRoleAssignment(t, db, roleAssignment{UserID: scoped.ID, Role: "department-admin", OrgUnit: root.Metadata.Name, Replica: true})

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

	t.Run("subtree assignment grants in the subtree", func(t *testing.T) {
		check(t, `SELECT api.check_org_role_binding($1::uuid, 'user_profile:read', $2)`, []any{scoped.ID, child.ID}, true)
		check(t, onSubject, []any{scoped.ID, "user_profile:read", target.ID}, true)
		check(t, onSubject, []any{scoped.ID, "role_assignment:create", target.ID}, true)
		check(t, onSubject, []any{scoped.ID, "user_profile:delete", target.ID}, false)
		check(t, onOrgUnit, []any{scoped.ID, "org_unit:read", root.ID}, true)
		check(t, onOrgUnit, []any{scoped.ID, "role:read", nil}, true)
		check(t, `SELECT api.has_permission($1::uuid, 'user_profile:read', NULL)`, []any{scoped.ID}, false)
	})

	t.Run("RLS follows global and subtree assignments", func(t *testing.T) {
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

		if n := count(scoped.ID, `SELECT count(*) FROM api.user_profiles WHERE id = $1::uuid`, target.ID); n != 1 {
			t.Errorf("subtree assignment does not see the target profile")
		}
		if n := count(scoped.ID, `SELECT count(*) FROM api.org_units WHERE id = $1`, child.ID); n != 1 {
			t.Errorf("subtree assignment does not see the OrgUnit")
		}
		if n := count(scoped.ID, `SELECT count(*) FROM api.roles`); n == 0 {
			t.Errorf("subtree assignment sees no roles")
		}
		if n := count(scoped.ID, `SELECT count(*) FROM api.user_profiles WHERE id = $1::uuid`, globalID); n != 0 {
			t.Errorf("subtree assignment sees a user outside the subtree")
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
		"role:read", "org_unit:read", "org_unit:assign-member",
		"org_unit:usage-read", "group_policy:read",
		"group_policy:create", "group_policy:update", "group_policy:delete",
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
