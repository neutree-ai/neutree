package dbtest

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// Tests for OrgUnit-scoped role bindings and delegation: a role assignment
// scoped to an OrgUnit subtree grants its permissions on the users and
// OrgUnits of the subtree only, Inactive OrgUnits grant nothing, and a
// delegated administrator cannot hand out more than they hold.

func TestOrgSubtreeBinding_Visibility(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]

	visibleProfiles := func(prelude func(tx *sql.Tx)) map[string]bool {
		t.Helper()

		seen := map[string]bool{}
		err := asUser(t, db, dadmin, prelude, func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT id::text FROM api.user_profiles`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen[id] = true
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatalf("listing profiles failed: %v", err)
		}

		short := map[string]bool{}
		for name, id := range o.users {
			if seen[id] {
				short[name] = true
			}
		}

		return short
	}

	t.Run("profiles in the subtree only", func(t *testing.T) {
		got := visibleProfiles(nil)
		for _, u := range []string{"dadmin", "ua", "ua1"} {
			if !got[u] {
				t.Errorf("%s is not visible", u)
			}
		}
		for _, u := range []string{"ub", "ux", "nobody"} {
			if got[u] {
				t.Errorf("%s is visible", u)
			}
		}

		if n := countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.user_profiles WHERE (metadata).name = 'admin'`); n != 0 {
			t.Errorf("the admin profile is visible")
		}
	})

	t.Run("profile update in the subtree only", func(t *testing.T) {
		for user, want := range map[string]int64{"ua1": 1, "ub": 0} {
			err := asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
				res, err := tx.Exec(`UPDATE api.user_profiles SET metadata = metadata WHERE id = $1::uuid`, o.users[user])
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n != want {
					t.Errorf("updating %s changed %d rows, want %d", user, n, want)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("updating %s failed: %v", user, err)
			}
		}
	})

	t.Run("OrgUnits and members in the subtree only", func(t *testing.T) {
		n := countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.org_units WHERE (spec).identity_source = $1`, o.source)
		if n != 2 { // a, a1; x is Inactive, root and b are outside
			t.Errorf("sees %d OrgUnits, want 2", n)
		}

		n = countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.org_unit_members WHERE identity_source = $1`, o.source)
		if n != 3 { // dadmin, ua (a), ua1 (a1)
			t.Errorf("sees %d members, want 3", n)
		}
	})

	t.Run("roles and own assignment are readable", func(t *testing.T) {
		if n := countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.roles WHERE (metadata).name IN ('admin', 'department-admin')`); n != 2 {
			t.Errorf("sees %d of the preset roles, want 2", n)
		}
		if n := countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.role_assignments WHERE (spec).user_id = $1::uuid`, dadmin); n != 1 {
			t.Errorf("sees %d own assignments, want 1", n)
		}
		if n := countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`); n != 0 {
			t.Errorf("sees the admin assignment")
		}
	})

	t.Run("an Inactive scope grants nothing", func(t *testing.T) {
		inactive := func(tx *sql.Tx) {
			mustTxExec(t, tx, `UPDATE api.org_units SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, o.units["a"])
		}
		got := visibleProfiles(inactive)
		if len(got) != 1 || !got["dadmin"] {
			t.Errorf("with a Inactive, visible = %v, want only dadmin (self)", got)
		}
		if n := countAs(t, db, dadmin, inactive, `SELECT count(*) FROM api.roles`); n != 0 {
			t.Errorf("with a Inactive, sees %d roles", n)
		}

		// A binding on the Inactive x grants nothing on its member.
		bindX := func(tx *sql.Tx) {
			mustTxExec(t, tx, `
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW('neu839-ub-x-' || $2, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
				        ROW($1::uuid, NULL, FALSE, 'department-admin', $3, NULL, NULL, NULL)::api.role_assignment_spec)`,
				o.users["ub"], o.suffix, o.unit("x"))
		}
		if n := countAs(t, db, o.users["ub"], bindX, `SELECT count(*) FROM api.user_profiles WHERE id = $1::uuid`, o.users["ux"]); n != 0 {
			t.Errorf("a binding on Inactive x sees its member")
		}
	})

	t.Run("visibility follows a department change", func(t *testing.T) {
		move := func(tx *sql.Tx) {
			mustTxExec(t, tx, `UPDATE api.org_unit_members SET org_unit_id = $2 WHERE user_id = $1::uuid`, o.users["ua1"], o.units["b"])
		}
		if got := visibleProfiles(move); got["ua1"] {
			t.Errorf("ua1 moved to b is still visible")
		}
		comeIn := func(tx *sql.Tx) {
			mustTxExec(t, tx, `UPDATE api.org_unit_members SET org_unit_id = $2 WHERE user_id = $1::uuid`, o.users["ub"], o.units["a1"])
		}
		if got := visibleProfiles(comeIn); !got["ub"] {
			t.Errorf("ub moved to a1 is not visible")
		}
	})
}

func TestOrgSubtreeBinding_NoWorkspaceAccess(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		SELECT 'v1', 'Role', ROW('neu839-ws-reader', NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		       ROW(NULL, ARRAY['endpoint:read', 'cluster:read', 'workspace:read']::api.permission_action[])::api.role_spec
		WHERE NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = 'neu839-ws-reader')`)
	o.bind(t, db, "ua", "neu839-ws-reader", "a")

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).role = 'neu839-ws-reader'`)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = 'neu839-ws-reader'`)
	})

	for _, ws := range []string{"", "default"} {
		for _, perm := range []string{"endpoint:read", "cluster:read", "workspace:read"} {
			if hasPermission(t, o.users["ua"], perm, ws) {
				t.Errorf("subtree binding grants %s in workspace %q", perm, ws)
			}
		}
		if hasPermission(t, o.users["dadmin"], "user_profile:read", ws) {
			t.Errorf("department-admin binding grants user_profile:read through has_permission (workspace %q)", ws)
		}
	}

	if n := countAs(t, db, o.users["ua"], nil, `SELECT count(*) FROM api.workspaces`); n != 0 {
		t.Errorf("subtree binding sees %d workspaces", n)
	}
}

func TestOrgSubtreeBinding_Delegation(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]

	grant := func(prelude func(tx *sql.Tx), user, role string, global bool, workspace, unit string) error {
		t.Helper()

		var ws, ou any
		if workspace != "" {
			ws = workspace
		}
		if unit != "" {
			ou = o.unit(unit)
		}

		return asUser(t, db, dadmin, prelude, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW($2::uuid, $3, $4, $5, $6, NULL, NULL, NULL)::api.role_assignment_spec)`,
				fmt.Sprintf("neu839-grant-%d", time.Now().UnixNano()), o.users[user], ws, global, role, ou)
			return err
		})
	}

	t.Run("a role within own permissions in the subtree", func(t *testing.T) {
		if err := grant(nil, "ua1", smallRole, false, "", "a1"); err != nil {
			t.Fatalf("granting %s to ua1 on a1 failed: %v", smallRole, err)
		}
		if err := grant(nil, "ua", "department-admin", false, "", "a"); err != nil {
			t.Fatalf("granting department-admin to ua on a failed: %v", err)
		}
	})

	t.Run("a larger role", func(t *testing.T) {
		wantErr(t, "granting admin", grant(nil, "ua1", "admin", false, "", "a1"), "10286")
		wantErr(t, "granting workspace-user", grant(nil, "ua1", "workspace-user", false, "", "a1"), "10286")
	})

	// The delegation trigger runs before the RLS check, so it is the one that
	// reports these.
	t.Run("a user outside the subtree", func(t *testing.T) {
		wantErr(t, "granting to ub", grant(nil, "ub", smallRole, false, "", "a"), "10284")
		wantErr(t, "granting to ux (Inactive OrgUnit)", grant(nil, "ux", smallRole, false, "", "a"), "10284")
		wantErr(t, "granting to nobody", grant(nil, "nobody", smallRole, false, "", "a"), "10284")
	})

	t.Run("a global or workspace scope", func(t *testing.T) {
		wantErr(t, "granting globally", grant(nil, "ua1", smallRole, true, "", ""), "10283")
		// A workspace scope is rejected before the delegation check.
		wantErr(t, "granting in a workspace", grant(nil, "ua1", smallRole, false, "default", ""), "10041")
	})

	t.Run("an OrgUnit scope outside the subtree", func(t *testing.T) {
		wantErr(t, "granting on b", grant(nil, "ua1", smallRole, false, "", "b"), "10284")
		wantErr(t, "granting on root", grant(nil, "ua1", smallRole, false, "", "root"), "10284")
		wantErr(t, "granting on Inactive x", grant(nil, "ua1", smallRole, false, "", "x"), "10284")
	})

	t.Run("a global administrator is not restricted", func(t *testing.T) {
		var adminID string
		if err := db.QueryRow(`SELECT id::text FROM api.user_profiles WHERE (metadata).name = 'admin'`).Scan(&adminID); err != nil {
			t.Fatal(err)
		}
		err := asUser(t, db, adminID, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW('neu839-by-admin', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW($1::uuid, NULL, FALSE, 'admin', $2, NULL, NULL, NULL)::api.role_assignment_spec)`, o.users["ub"], o.unit("root"))
			return err
		})
		if err != nil {
			t.Fatalf("admin granting on root failed: %v", err)
		}
	})

	// Revoking. Each assignment is stored as postgres in the prelude.
	seed := func(name, user, role string, global bool, workspace, unit string) func(tx *sql.Tx) {
		return func(tx *sql.Tx) {
			var ws, ou any
			if workspace != "" {
				ws = workspace
			}
			if unit != "" {
				ou = o.unit(unit)
			}
			mustTxExec(t, tx, `
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW($2::uuid, $3, $4, $5, $6, NULL, NULL, NULL)::api.role_assignment_spec)`,
				name, o.users[user], ws, global, role, ou)
		}
	}

	revoke := func(prelude func(tx *sql.Tx), name string, soft bool) error {
		return asUser(t, db, dadmin, prelude, func(tx *sql.Tx) error {
			var (
				res sql.Result
				err error
			)
			if soft {
				res, err = tx.Exec(`UPDATE api.role_assignments SET metadata.deletion_timestamp = now() WHERE (metadata).name = $1`, name)
			} else {
				res, err = tx.Exec(`DELETE FROM api.role_assignments WHERE (metadata).name = $1`, name)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return fmt.Errorf("revoked %d rows", n)
			}
			return nil
		})
	}

	t.Run("revoke within scope", func(t *testing.T) {
		for _, soft := range []bool{true, false} {
			if err := revoke(seed("neu839-r1", "ua1", smallRole, false, "", "a1"), "neu839-r1", soft); err != nil {
				t.Fatalf("revoking (soft=%v) failed: %v", soft, err)
			}
		}
	})

	t.Run("revoke outside scope", func(t *testing.T) {
		for _, soft := range []bool{true, false} {
			wantErr(t, "revoking a global assignment",
				revoke(seed("neu839-r2", "ua", "workspace-user", true, "", ""), "neu839-r2", soft), "10283")
			wantErr(t, "revoking an assignment on root",
				revoke(seed("neu839-r3", "ua", smallRole, false, "", "root"), "neu839-r3", soft), "10284")
			wantErr(t, "revoking a larger role",
				revoke(seed("neu839-r4", "ua1", "admin", false, "", "a1"), "neu839-r4", soft), "10286")
		}
		// An assignment of a user outside the subtree is not even visible.
		wantErr(t, "revoking ub's assignment",
			revoke(seed("neu839-r5", "ub", smallRole, false, "", "b"), "neu839-r5", false), "revoked 0 rows")
	})

	t.Run("update cannot enlarge", func(t *testing.T) {
		err := asUser(t, db, dadmin, seed("neu839-u1", "ua1", smallRole, false, "", "a1"), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE api.role_assignments SET spec.role = 'admin' WHERE (metadata).name = 'neu839-u1'`)
			return err
		})
		wantErr(t, "changing the role to admin", err, "10286")

		err = asUser(t, db, dadmin, seed("neu839-u2", "ua1", smallRole, false, "", "a1"), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE api.role_assignments SET spec.org_unit = NULL, spec.global = TRUE WHERE (metadata).name = 'neu839-u2'`)
			return err
		})
		wantErr(t, "changing the scope to global", err, "10283")
	})

	t.Run("scope validation", func(t *testing.T) {
		err := asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW('neu839-v1', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW($1::uuid, NULL, TRUE, $2, $3, NULL, NULL, NULL)::api.role_assignment_spec)`, o.users["ua1"], smallRole, o.unit("a1"))
			return err
		})
		wantErr(t, "global with an OrgUnit", err, "10281")

		err = asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW('neu839-v2', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW($1::uuid, NULL, NULL, $2, 'no-such-unit', NULL, NULL, NULL)::api.role_assignment_spec)`, o.users["ua1"], smallRole)
			return err
		})
		wantErr(t, "an unknown OrgUnit", err, "10282")
	})
}
