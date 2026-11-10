package dbtest

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// Tests for group subjects with OrgUnit scopes and delegation. Uses the
// tree of setupOrgTree; dadmin is department-admin on a.

func TestOrgSubtreeGroupBinding_OrgUnitScope(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	t.Run("an OrgUnit-scoped OrgUnit assignment covers the subtree", func(t *testing.T) {
		bind := groupBinding(t, "OrgUnit", o.unit("a"), "department-admin", o.unit("a"))
		onSubject := `SELECT api.has_permission_on_subject($1::uuid, 'user_profile:read', $2::uuid)`

		for _, who := range []string{"ua", "ua1"} {
			if !checkIn(t, db, bind, onSubject, o.users[who], o.users["ua1"]) {
				t.Errorf("%s (member of the subtree) cannot read ua1", who)
			}
			if checkIn(t, db, bind, onSubject, o.users[who], o.users["ub"]) {
				t.Errorf("%s reads ub, outside the scope", who)
			}
		}

		for _, who := range []string{"ub", "ux"} {
			if checkIn(t, db, bind, onSubject, o.users[who], o.users["ua1"]) {
				t.Errorf("%s (not an Active member of the subtree) got the assignment", who)
			}
		}
	})

	t.Run("a group assignment makes a delegated administrator", func(t *testing.T) {
		bind := groupBinding(t, "OrgUnit", o.unit("a1"), "department-admin", o.unit("a1"))
		err := asUser(t, db, o.users["ua1"], bind, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW('grb-by-group', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW(NULL, NULL, FALSE, $1, $2, 'OrgUnit', $2, NULL)::api.role_assignment_spec)`, smallRole, o.unit("a1"))
			return err
		})
		if err != nil {
			t.Errorf("a department-admin through a group granting in its subtree failed: %v", err)
		}
	})
}

func TestOrgSubtreeGroupBinding_Delegation(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]

	team := "grb-dteam-" + o.suffix
	manualTeam(t, db, team)

	grant := func(kind, subject, role string, global bool, workspace, orgUnit any) error {
		t.Helper()

		return asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW(NULL, $2, $3, $4, $5, $6, $7, NULL)::api.role_assignment_spec)`,
				fmt.Sprintf("grb-grant-%d", time.Now().UnixNano()), workspace, global, role, orgUnit, kind, subject)
			return err
		})
	}

	t.Run("an OrgUnit in the subtree, OrgUnit scope", func(t *testing.T) {
		if err := grant("OrgUnit", o.unit("a1"), smallRole, false, nil, o.unit("a1")); err != nil {
			t.Errorf("granting to a1 on a1 failed: %v", err)
		}
		if err := grant("OrgUnit", o.unit("a1"), smallRole, false, nil, o.unit("a")); err != nil {
			t.Errorf("granting to a1 on a failed: %v", err)
		}
	})

	t.Run("a Team", func(t *testing.T) {
		wantErr(t, "granting to a Team on a", grant("Team", team, smallRole, false, nil, o.unit("a")), "10297")
		wantErr(t, "granting to a Team globally", grant("Team", team, smallRole, true, nil, nil), "10297")
	})

	t.Run("an OrgUnit outside the subtree", func(t *testing.T) {
		wantErr(t, "granting to b", grant("OrgUnit", o.unit("b"), smallRole, false, nil, o.unit("a")), "10284")
		wantErr(t, "granting to root", grant("OrgUnit", o.unit("root"), smallRole, false, nil, o.unit("a")), "10284")
		wantErr(t, "granting to Inactive x", grant("OrgUnit", o.unit("x"), smallRole, false, nil, o.unit("a")), "10284")
	})

	t.Run("not an OrgUnit scope, or more than held", func(t *testing.T) {
		wantErr(t, "granting to a1 globally", grant("OrgUnit", o.unit("a1"), smallRole, true, nil, nil), "10283")
		// A workspace scope is rejected before the delegation check.
		wantErr(t, "granting to a1 in a workspace", grant("OrgUnit", o.unit("a1"), smallRole, false, "default", nil), "10041")
		wantErr(t, "granting admin to a1", grant("OrgUnit", o.unit("a1"), "admin", false, nil, o.unit("a1")), "10286")
	})

	t.Run("Team assignments are not visible to a delegated administrator", func(t *testing.T) {
		bind := groupBinding(t, "Team", team, smallRole, "")
		if n := countAs(t, db, dadmin, bind, `SELECT count(*) FROM api.role_assignments WHERE (spec).subject_kind = 'Team'`); n != 0 {
			t.Errorf("sees %d Team assignments", n)
		}

		inSubtree := groupBinding(t, "OrgUnit", o.unit("a1"), smallRole, o.unit("a1"))
		if n := countAs(t, db, dadmin, inSubtree, `SELECT count(*) FROM api.role_assignments WHERE (spec).subject_name = $1`, o.unit("a1")); n != 1 {
			t.Errorf("sees %d assignments of a1, want 1", n)
		}
	})
}

// A delegated administrator moving a user into an OrgUnit hands over that
// OrgUnit's assignments, so the move is checked like granting them.
func TestOrgSubtreeGroupBinding_DelegatedMove(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]
	local := o.users["nobody"]

	move := func(binding func(tx *sql.Tx)) error {
		return asUser(t, db, dadmin, func(tx *sql.Tx) {
			mustTxExec(t, tx, `INSERT INTO api.org_unit_members (user_id, org_unit_id) VALUES ($1, $2)`, local, o.units["a"])
			binding(tx)
		}, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE api.org_unit_members SET org_unit_id = $2 WHERE user_id = $1`, local, o.units["a1"])
			return err
		})
	}

	if err := move(groupBinding(t, "OrgUnit", o.unit("a1"), smallRole, o.unit("a1"))); err != nil {
		t.Errorf("moving into an OrgUnit whose assignment the admin could grant failed: %v", err)
	}

	wantErr(t, "moving into an OrgUnit bound to admin",
		move(groupBinding(t, "OrgUnit", o.unit("a1"), "admin", o.unit("a1"))), "10296")
	wantErr(t, "moving into an OrgUnit with a global assignment",
		move(groupBinding(t, "OrgUnit", o.unit("a1"), smallRole, "")), "10296")

	// a is on both paths, so its assignment is not a new grant.
	if err := move(groupBinding(t, "OrgUnit", o.unit("a"), "admin", o.unit("a"))); err != nil {
		t.Errorf("moving under an OrgUnit the user is already in failed: %v", err)
	}
}

// A delegated administrator reads the effective assignments
// of users in their subtree only.
func TestOrgSubtreeGroupBinding_EffectiveRPCSubtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	bind := groupBinding(t, "OrgUnit", o.unit("a"), smallRole, o.unit("a"))
	list := func(user string) (int, error) {
		n := 0
		err := asUser(t, db, o.users["dadmin"], bind, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*) FROM api.get_user_effective_role_assignments($1::uuid) WHERE via_kind = 'OrgUnit'`, o.users[user]).Scan(&n)
		})

		return n, err
	}

	if n, err := list("ua1"); err != nil || n != 1 {
		t.Errorf("ua1 (in the subtree) = %d, %v; want the OrgUnit assignment", n, err)
	}

	_, err := list("ub")
	wantErr(t, "reading ub (outside the subtree)", err, "role_assignment:read on this user is required")
}
