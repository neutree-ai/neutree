package dbtest

import (
	"database/sql"
	"testing"
)

// Tests for Team-scoped role assignments: spec.team grants a role on one
// Team only, through api.has_permission_on_team.

// bindTeam assigns role to the user on the Team named team, as postgres.
func bindTeam(t *testing.T, db *sql.DB, userID, role, team string) {
	t.Helper()

	seedRoleAssignment(t, db, roleAssignment{UserID: userID, Role: role, SubjectKind: "User", Team: team})
}

func onTeam(t *testing.T, tx *sql.Tx, userID, perm string, teamID any) bool {
	t.Helper()

	var ok bool
	if err := tx.QueryRow(`SELECT api.has_permission_on_team($1::uuid, $2::api.permission_action, $3::integer)`,
		userID, perm, teamID).Scan(&ok); err != nil {
		t.Fatalf("has_permission_on_team failed: %v", err)
	}

	return ok
}

func TestTeamScopedRoleBinding_Grants(t *testing.T) {
	db := GetTestDB(t)
	teamA := newOrgName("tsb-a")
	teamB := newOrgName("tsb-b")
	idA := manualTeam(t, db, teamA)
	idB := manualTeam(t, db, teamB)

	user := rawUser(t, db, "tsb-user")
	role := customRole(t, db, "team:assign-member")
	bindTeam(t, db, user, role, teamA)

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	cases := []struct {
		name string
		got  bool
		want bool
	}{
		{"on its Team", onTeam(t, tx, user, "team:assign-member", idA), true},
		{"on another Team", onTeam(t, tx, user, "team:assign-member", idB), false},
		{"on any Team", onTeam(t, tx, user, "team:assign-member", nil), true},
		{"another permission on its Team", onTeam(t, tx, user, "team:read", idA), false},
		{"globally", hasPermission(t, user, "team:assign-member", ""), false},
	}

	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	var anyScope bool
	if err := tx.QueryRow(`SELECT api.has_permission_in_any_scope($1::uuid, 'team:assign-member')`, user).Scan(&anyScope); err != nil || !anyScope {
		t.Errorf("has_permission_in_any_scope = %v, %v; want true", anyScope, err)
	}

	// An Inactive Team grants nothing (a manual Team cannot be Inactive, so
	// the write checks are skipped for this rolled-back update).
	mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
	mustTxExec(t, tx, `UPDATE api.teams SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, idA)
	mustTxExec(t, tx, `SET LOCAL session_replication_role = DEFAULT`)

	if onTeam(t, tx, user, "team:assign-member", idA) {
		t.Error("an Inactive Team still grants")
	}
}

func TestTeamScopedRoleBinding_Writes(t *testing.T) {
	db := GetTestDB(t)
	team := newOrgName("tsb-w")
	teamID := manualTeam(t, db, team)
	user := rawUser(t, db, "tsb-w-user")
	role := customRole(t, db, "team:read")

	insert := func(tx *sql.Tx, global any, workspace any, orgUnit any, teamName string) error {
		_, err := tx.Exec(`
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
			        ROW($2::uuid, $3, $4, $5, $6, 'User', NULL, $7)::api.role_assignment_spec)`,
			newOrgName("tsb-w"), user, workspace, global, role, orgUnit, teamName)

		return err
	}

	t.Run("a Team scope is stored as neither global nor in a workspace", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		if err := insert(tx, nil, nil, nil, team); err != nil {
			t.Fatalf("insert failed: %v", err)
		}

		var global bool
		var stored string
		if err := tx.QueryRow(`SELECT (spec).global, (spec).team FROM api.role_assignments WHERE (spec).user_id = $1`, user).
			Scan(&global, &stored); err != nil {
			t.Fatal(err)
		}

		if global || stored != team {
			t.Fatalf("stored global=%v team=%s", global, stored)
		}

		// A PATCH whose spec has no scope keeps the stored one.
		mustTxExec(t, tx, `
			UPDATE api.role_assignments
			SET spec = ROW((spec).user_id, NULL, NULL, (spec).role, NULL, 'User', NULL, NULL)::api.role_assignment_spec
			WHERE (spec).user_id = $1`, user)

		if err := tx.QueryRow(`SELECT (spec).team FROM api.role_assignments WHERE (spec).user_id = $1`, user).Scan(&stored); err != nil || stored != team {
			t.Fatalf("after a PATCH without scope: team %q, %v", stored, err)
		}

		if !onTeam(t, tx, user, "team:read", teamID) {
			t.Fatal("the stored assignment does not grant on its Team")
		}
	})

	for _, tc := range []struct {
		name                       string
		global, workspace, orgUnit any
		team                       string
		code                       string
	}{
		{"global and a Team", true, nil, nil, team, "10312"},
		{"a workspace and a Team", false, "default", nil, team, "10312"},
		{"an unknown Team", nil, nil, nil, "no-such-team", "10313"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()

			wantErr(t, tc.name, insert(tx, tc.global, tc.workspace, tc.orgUnit, tc.team), tc.code)
		})
	}

	t.Run("a Team referenced by a Team scope cannot be deleted", func(t *testing.T) {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		if err := insert(tx, nil, nil, nil, team); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(`DELETE FROM api.teams WHERE id = $1`, teamID)
		wantErr(t, "deleting the Team", err, "10295")
	})
}

// Only a global administrator grants a Team scope; an OrgUnit administrator
// is refused even for a user of their subtree.
func TestTeamScopedRoleBinding_Delegation(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	team := newOrgName("tsb-d")
	manualTeam(t, db, team)

	admin := rawUser(t, db, "tsb-admin")
	bindGlobal(t, db, admin, customRole(t, db, "role_assignment:create", "role_assignment:read", "user_profile:read"))

	insertAs := func(actor string) error {
		return asUser(t, db, actor, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
				VALUES ('v1', 'RoleAssignment', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
				        ROW($2::uuid, NULL, FALSE, $3, NULL, 'User', NULL, $4)::api.role_assignment_spec)`,
				newOrgName("tsb-d"), o.users["ua"], smallRole, team)

			return err
		})
	}

	wantErr(t, "department-admin grants a Team scope", insertAs(o.users["dadmin"]), "10314")

	if err := insertAs(admin); err != nil {
		t.Fatalf("a global administrator granting a Team scope failed: %v", err)
	}
}
