package dbtest

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// What OrgUnit-scoped bindings unlock and what they do not: UI scope
// checks (has_permission_in_any_scope), hand-written memberships, subtree
// usage and policy reads, and the group features that stay global-only.

func TestHasPermissionInAnyScope_Subtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	var adminID string
	if err := db.QueryRow(`SELECT id::text FROM api.user_profiles WHERE (metadata).name = 'admin'`).Scan(&adminID); err != nil {
		t.Fatalf("finding the admin user failed: %v", err)
	}

	bindOnInactive := func(tx *sql.Tx) {
		mustTxExec(t, tx, `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment', ROW('neu841-ub-x-' || $2, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
			        ROW($1::uuid, NULL, FALSE, 'department-admin', $3, NULL, NULL, NULL)::api.role_assignment_spec)`,
			o.users["ub"], o.suffix, o.unit("x"))
	}
	scopeInactive := func(tx *sql.Tx) {
		mustTxExec(t, tx, `UPDATE api.org_units SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, o.units["a"])
	}

	cases := []struct {
		name    string
		user    string
		prelude func(tx *sql.Tx)
		perm    string
		want    bool
	}{
		{"global assignment", adminID, nil, "org_unit:read", true},
		{"subtree assignment with the permission", o.users["dadmin"], nil, "org_unit:read", true},
		{"subtree assignment, another permission of the role", o.users["dadmin"], nil, "role_assignment:create", true},
		{"subtree assignment without the permission", o.users["dadmin"], nil, "endpoint:read", false},
		{"no assignment", o.users["nobody"], nil, "org_unit:read", false},
		{"subtree assignment on an Inactive OrgUnit", o.users["ub"], bindOnInactive, "org_unit:read", false},
		{"subtree assignment whose OrgUnit went Inactive", o.users["dadmin"], scopeInactive, "org_unit:read", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got bool
			if err := asUser(t, db, c.user, c.prelude, func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT api.has_permission_in_any_scope($1::uuid, $2::api.permission_action)`,
					c.user, c.perm).Scan(&got)
			}); err != nil {
				t.Fatalf("has_permission_in_any_scope failed: %v", err)
			}
			if got != c.want {
				t.Fatalf("has_permission_in_any_scope(%s) = %v, want %v", c.perm, got, c.want)
			}
		})
	}
}

func TestOrgUnitMemberWrite_Subtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]
	local := o.users["nobody"]

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatalf("reading the admin user failed: %v", err)
	}

	// A hand-written membership of the local user, set up as postgres.
	in := func(unit string) func(tx *sql.Tx) {
		return func(tx *sql.Tx) {
			mustTxExec(t, tx, `INSERT INTO api.org_unit_members (user_id, org_unit_id) VALUES ($1, $2)`, local, o.units[unit])
		}
	}

	write := func(actor string, prelude func(tx *sql.Tx), query string, args ...any) (int64, error) {
		t.Helper()

		var n int64
		err := asUser(t, db, actor, prelude, func(tx *sql.Tx) error {
			res, err := tx.Exec(query, args...)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			return nil
		})

		return n, err
	}

	insert := `INSERT INTO api.org_unit_members (user_id, org_unit_id) VALUES ($1, $2)`
	move := `UPDATE api.org_unit_members SET org_unit_id = $2 WHERE user_id = $1`
	remove := `DELETE FROM api.org_unit_members WHERE user_id = $1`

	t.Run("a global assigner inserts, moves and removes anywhere", func(t *testing.T) {
		if n, err := write(admin, nil, insert, local, o.units["b"]); err != nil || n != 1 {
			t.Errorf("insert = %d, %v", n, err)
		}
		if n, err := write(admin, in("a"), move, local, o.units["b"]); err != nil || n != 1 {
			t.Errorf("move a -> b = %d, %v", n, err)
		}
		if n, err := write(admin, in("b"), remove, local); err != nil || n != 1 {
			t.Errorf("remove from b = %d, %v", n, err)
		}
	})

	t.Run("department-admin cannot insert, even into the subtree", func(t *testing.T) {
		_, err := write(dadmin, nil, insert, local, o.units["a1"])
		wantErr(t, "insert into a1", err, "row-level security")
	})

	t.Run("department-admin moves within the subtree", func(t *testing.T) {
		if n, err := write(dadmin, in("a"), move, local, o.units["a1"]); err != nil || n != 1 {
			t.Errorf("move a -> a1 = %d, %v", n, err)
		}
	})

	t.Run("department-admin cannot move out of the subtree", func(t *testing.T) {
		_, err := write(dadmin, in("a"), move, local, o.units["b"])
		wantErr(t, "move a -> b", err, "row-level security")
	})

	t.Run("department-admin cannot pull a user in", func(t *testing.T) {
		if n, err := write(dadmin, in("b"), move, local, o.units["a"]); err != nil || n != 0 {
			t.Errorf("move b -> a = %d, %v; want no row", n, err)
		}
	})

	t.Run("department-admin cannot change whose membership it is", func(t *testing.T) {
		prelude := func(tx *sql.Tx) {
			in("a")(tx)
			mustTxExec(t, tx, `DELETE FROM api.org_unit_members WHERE user_id = $1`, o.users["ub"])
		}
		_, err := write(dadmin, prelude, `UPDATE api.org_unit_members SET user_id = $2 WHERE user_id = $1`, local, o.users["ub"])
		wantErr(t, "changing the user", err, "10287")
	})

	t.Run("department-admin removes within the subtree", func(t *testing.T) {
		if n, err := write(dadmin, in("a1"), remove, local); err != nil || n != 1 {
			t.Errorf("remove from a1 = %d, %v", n, err)
		}
	})

	t.Run("department-admin cannot remove outside the subtree", func(t *testing.T) {
		if n, err := write(dadmin, in("b"), remove, local); err != nil || n != 0 {
			t.Errorf("remove from b = %d, %v; want no row", n, err)
		}
	})

	t.Run("an Inactive OrgUnit is still refused", func(t *testing.T) {
		_, err := write(dadmin, in("a"), move, local, o.units["x"])
		wantErr(t, "move a -> x", err, "10277")
	})

	t.Run("an external user is still refused", func(t *testing.T) {
		prelude := func(tx *sql.Tx) {
			// A hand-written row that predates the external link; the
			// member trigger would refuse writing it now.
			mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
			in("a")(tx)
			mustTxExec(t, tx, `INSERT INTO api.external_identities (source, external_id, user_id) VALUES ($1, $2, $3)`,
				"ldap:"+o.source, "ext-"+o.suffix, local)
			mustTxExec(t, tx, `SET LOCAL session_replication_role = origin`)
		}
		_, err := write(dadmin, prelude, move, local, o.units["a1"])
		wantErr(t, "move an external user a -> a1", err, "10278")
	})
}

func TestUserProfileDelete_NotBySubtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	target := o.users["ua1"]

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatalf("reading the admin user failed: %v", err)
	}

	const deleter = "neu839-user-deleter"
	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		SELECT 'v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		       ROW(NULL, ARRAY['user_profile:read', 'user_profile:delete']::api.permission_action[])::api.role_spec
		WHERE NOT EXISTS (SELECT 1 FROM api.roles WHERE (metadata).name = $1)`, deleter)
	o.bind(t, db, "ub", deleter, "a")

	write := func(actor, query string) (int64, error) {
		t.Helper()

		var n int64
		err := asUser(t, db, actor, nil, func(tx *sql.Tx) error {
			res, err := tx.Exec(query, target)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			return nil
		})

		return n, err
	}

	softDelete := `UPDATE api.user_profiles SET metadata.deletion_timestamp = now() WHERE id = $1`
	hardDelete := `DELETE FROM api.user_profiles WHERE id = $1`

	t.Run("a subtree-bound user_profile:delete deletes nobody in the subtree", func(t *testing.T) {
		if n := countAs(t, db, o.users["ub"], nil, `SELECT count(*) FROM api.user_profiles WHERE id = $1`, target); n != 1 {
			t.Fatalf("the subtree binding does not see the target (%d rows); the checks below would prove nothing", n)
		}
		if n, err := write(o.users["ub"], softDelete); err == nil && n != 0 {
			t.Errorf("soft delete = %d rows, want none", n)
		}
		if n, err := write(o.users["ub"], hardDelete); err != nil || n != 0 {
			t.Errorf("delete = %d, %v; want no row", n, err)
		}
	})

	t.Run("a global admin deletes the same user", func(t *testing.T) {
		if n, err := write(admin, softDelete); err != nil || n != 1 {
			t.Errorf("soft delete = %d, %v", n, err)
		}
		if n, err := write(admin, hardDelete); err != nil || n != 1 {
			t.Errorf("delete = %d, %v", n, err)
		}
	})
}

func TestSubtreeRead_UsageByGroup(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	ws := "d82-5-ws-" + o.suffix

	// ua's 5 tokens were recorded while ua was in b.
	usage := func(tx *sql.Tx) {
		for _, r := range []struct {
			user, unit string
			tokens     int
		}{{"ua", "a", 10}, {"ua", "b", 5}, {"ua1", "a1", 20}, {"ux", "x", 40}, {"ub", "b", 80}} {
			mustTxExec(t, tx, `
				INSERT INTO api.group_daily_usage (usage_date, user_id, org_unit_id, workspace,
				    endpoint_type, endpoint_name, model_name, requests, total_tokens)
				VALUES (CURRENT_DATE, $1, $2, $3, 'endpoint', 'ep', 'm', 1, $4)`,
				o.users[r.user], o.units[r.unit], ws, r.tokens)
		}
	}

	totals := func(who, kind string) (map[string]int, error) {
		out := map[string]int{}
		err := asUser(t, db, who, usage, func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT name, usage FROM api.get_usage_by_group(CURRENT_DATE, CURRENT_DATE, $1, NULL, $2)`, kind, ws)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var name string
				var n int
				if err := rows.Scan(&name, &n); err != nil {
					return err
				}
				out[name] += n
			}

			return rows.Err()
		})

		return out, err
	}

	dadmin := o.users["dadmin"]

	t.Run("by user: the usage recorded in the Active subtree", func(t *testing.T) {
		got, err := totals(dadmin, "User")
		if err != nil {
			t.Fatal(err)
		}

		want := map[string]int{o.users["ua"]: 10, o.users["ua1"]: 20}
		if len(got) != len(want) || got[o.users["ua"]] != 10 || got[o.users["ua1"]] != 20 {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("by OrgUnit: units of the subtree only", func(t *testing.T) {
		got, err := totals(dadmin, "OrgUnit")
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 || got[o.unit("a")] != 30 || got[o.unit("a1")] != 20 {
			t.Errorf("got %v, want a 30 and a1 20", got)
		}
	})

	t.Run("by Team: refused", func(t *testing.T) {
		_, err := totals(dadmin, "Team")
		wantErr(t, "dadmin by Team", err, "required to read usage by Team")
	})

	t.Run("no binding: refused", func(t *testing.T) {
		_, err := totals(o.users["ua"], "User")
		wantErr(t, "ua by user", err, "global workspace:usage-read is required")
	})

	t.Run("global admin: unchanged", func(t *testing.T) {
		var admin string
		if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
			t.Fatal(err)
		}

		got, err := totals(admin, "OrgUnit")
		if err != nil {
			t.Fatal(err)
		}

		if got[o.unit("root")] != 155 || got[o.unit("x")] != 40 || got[o.unit("b")] != 85 {
			t.Errorf("got %v", got)
		}
	})
}

// The p_org_unit and p_team filters of get_usage_by_group only narrow.
func TestUsageByGroup_Filters(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	ws := "usage-filter-ws-" + o.suffix
	team := "usage-filter-team-" + o.suffix

	// ua and ub are in the team; ua's 5 tokens were recorded while ua was in b.
	usage := func(tx *sql.Tx) {
		var teamID int
		if err := tx.QueryRow(`
			INSERT INTO api.teams (api_version, kind, metadata)
			VALUES ('v1', 'Team', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata)
			RETURNING id`, team).Scan(&teamID); err != nil {
			t.Fatalf("creating the team failed: %v", err)
		}

		for _, r := range []struct {
			user, unit string
			inTeam     bool
			tokens     int
		}{
			{"ua", "a", true, 10}, {"ua", "b", true, 5}, {"ua1", "a1", false, 20},
			{"ux", "x", false, 40}, {"ub", "b", true, 80},
		} {
			teams := "{}"
			if r.inTeam {
				teams = fmt.Sprintf("{%d}", teamID)
			}
			mustTxExec(t, tx, `
				INSERT INTO api.group_daily_usage (usage_date, user_id, org_unit_id, team_ids, workspace,
				    endpoint_type, endpoint_name, model_name, requests, total_tokens)
				VALUES (CURRENT_DATE, $1, $2, $3::integer[], $4, 'endpoint', 'ep', 'm', 1, $5)`,
				o.users[r.user], o.units[r.unit], teams, ws, r.tokens)
		}
	}

	totals := func(who, kind string, orgUnit, team any) (map[string]int, error) {
		out := map[string]int{}
		err := asUser(t, db, who, usage, func(tx *sql.Tx) error {
			rows, err := tx.Query(`
				SELECT name, usage FROM api.get_usage_by_group(
				    p_start_date => CURRENT_DATE, p_end_date => CURRENT_DATE, p_kind => $1,
				    p_workspace => $2, p_org_unit => $3, p_team => $4)`, kind, ws, orgUnit, team)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var name string
				var n int
				if err := rows.Scan(&name, &n); err != nil {
					return err
				}
				out[name] += n
			}

			return rows.Err()
		})

		return out, err
	}

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	dadmin := o.users["dadmin"]
	u := o.users

	want := func(t *testing.T, got map[string]int, err error, want map[string]int) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}

	t.Run("subtree filter: users recorded under a and below", func(t *testing.T) {
		got, err := totals(admin, "User", o.unit("a"), nil)
		want(t, got, err, map[string]int{u["ua"]: 10, u["ua1"]: 20, u["ux"]: 40})
	})

	t.Run("subtree filter by OrgUnit: units of the subtree only", func(t *testing.T) {
		got, err := totals(admin, "OrgUnit", o.unit("a"), nil)
		want(t, got, err, map[string]int{o.unit("a"): 70, o.unit("a1"): 20, o.unit("x"): 40})
	})

	t.Run("unknown OrgUnit: no rows", func(t *testing.T) {
		got, err := totals(admin, "User", "no-such-unit-"+o.suffix, nil)
		want(t, got, err, map[string]int{})
	})

	t.Run("team filter: users of the team", func(t *testing.T) {
		got, err := totals(admin, "User", nil, team)
		want(t, got, err, map[string]int{u["ua"]: 15, u["ub"]: 80})
	})

	t.Run("both filters", func(t *testing.T) {
		got, err := totals(admin, "User", o.unit("b"), team)
		want(t, got, err, map[string]int{u["ua"]: 5, u["ub"]: 80})
	})

	t.Run("department admin: the filter does not widen", func(t *testing.T) {
		got, err := totals(dadmin, "User", o.unit("root"), nil)
		want(t, got, err, map[string]int{u["ua"]: 10, u["ua1"]: 20})

		got, err = totals(dadmin, "User", o.unit("b"), nil)
		want(t, got, err, map[string]int{})

		got, err = totals(dadmin, "User", o.unit("a1"), nil)
		want(t, got, err, map[string]int{u["ua1"]: 20})
	})

	t.Run("department admin with p_team: refused", func(t *testing.T) {
		_, err := totals(dadmin, "User", nil, team)
		wantErr(t, "dadmin with p_team", err, "required to read usage by Team")
	})
}

func TestSubtreeRead_GroupPolicy(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	dadmin := o.users["dadmin"]

	mustExec(t, db, `
		INSERT INTO api.group_policies (api_version, kind, metadata, spec)
		VALUES ('v1', 'GroupPolicy', ROW(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
		        ROW('OrgUnit', $1, '[{"model": "m"}]'::jsonb, NULL, '{"limit": 100}'::jsonb)::api.group_policy_spec)`, o.unit("root"))

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.group_policies WHERE (spec).subject_name = $1`, o.unit("root"))
	})

	policyOf := func(user string) (string, error) {
		var out string
		err := asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT api.get_user_group_policy($1::uuid, NULL)::text`, o.users[user]).Scan(&out)
		})

		return out, err
	}

	t.Run("users of the Active subtree", func(t *testing.T) {
		for _, user := range []string{"ua", "ua1"} {
			got, err := policyOf(user)
			if err != nil {
				t.Fatalf("%s: %v", user, err)
			}

			if got == "" || !strings.Contains(got, o.unit("root")) || !strings.Contains(got, `"token_limit": 100`) {
				t.Errorf("%s: %s", user, got)
			}
		}
	})

	t.Run("users outside it or in an Inactive unit", func(t *testing.T) {
		for _, user := range []string{"ub", "ux", "nobody"} {
			_, err := policyOf(user)
			wantErr(t, user, err, "group_policy:read is required")
		}
	})

	t.Run("policy rows and pools stay global-only", func(t *testing.T) {
		if n := countAs(t, db, dadmin, nil, `SELECT count(*) FROM api.group_policies`); n != 0 {
			t.Errorf("dadmin reads %d policy rows, want 0", n)
		}

		err := asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO api.group_policies (api_version, kind, metadata, spec)
				VALUES ('v1', 'GroupPolicy', ROW(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
				        ROW('OrgUnit', $1, NULL, '[{"model": "m"}]'::jsonb, NULL)::api.group_policy_spec)`, o.unit("a1"))
			return err
		})
		wantErr(t, "dadmin creating a policy on a1", err, "row-level security")

		err = asUser(t, db, dadmin, nil, func(tx *sql.Tx) error {
			var n int
			return tx.QueryRow(`SELECT count(*) FROM api.get_group_token_pools('OrgUnit', $1)`, o.unit("a")).Scan(&n)
		})
		wantErr(t, "dadmin reading pools", err, "global group_policy:read is required")
	})
}

// Team writes count only when assigned globally.
func TestTeamWrite_NotBySubtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	role := "neu842-team-writer-" + o.suffix

	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES ('v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		        ROW(NULL, ARRAY['team:read', 'team:create', 'team:update', 'team:delete', 'team:assign-member']::api.permission_action[])::api.role_spec)`, role)

	// ua: the writer role on subtree a (ua is a member of a).
	o.bind(t, db, "ua", role, "a")

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).role = $1`, role)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = $1`, role)
	})

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatalf("reading the admin user failed: %v", err)
	}

	team := "neu842-team-" + o.suffix
	insertTeam := `INSERT INTO api.teams (api_version, kind, metadata) VALUES ('v1', 'Team', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata)`

	// A manual Team created as postgres inside the checked transaction.
	withTeam := func(tx *sql.Tx) { mustTxExec(t, tx, insertTeam, team) }

	exec := func(actor string, prelude func(tx *sql.Tx), query string, args ...any) (int64, error) {
		t.Helper()

		var n int64
		err := asUser(t, db, actor, prelude, func(tx *sql.Tx) error {
			res, err := tx.Exec(query, args...)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			return nil
		})

		return n, err
	}

	update := `UPDATE api.teams SET metadata.display_name = 'renamed' WHERE (metadata).name = $1`
	remove := `DELETE FROM api.teams WHERE (metadata).name = $1`
	addMember := `INSERT INTO api.team_members (team_id, user_id) SELECT id, $2::uuid FROM api.teams WHERE (metadata).name = $1`

	t.Run("a global writer writes", func(t *testing.T) {
		if n, err := exec(admin, nil, insertTeam, team); err != nil || n != 1 {
			t.Errorf("create = %d, %v", n, err)
		}
		if n, err := exec(admin, withTeam, update, team); err != nil || n != 1 {
			t.Errorf("update = %d, %v", n, err)
		}
		if n, err := exec(admin, withTeam, addMember, team, o.users["ub"]); err != nil || n != 1 {
			t.Errorf("add member = %d, %v", n, err)
		}
		if n, err := exec(admin, withTeam, remove, team); err != nil || n != 1 {
			t.Errorf("delete = %d, %v", n, err)
		}
	})

	for _, who := range []string{"ua"} {
		actor := o.users[who]

		t.Run(who+" writes nothing", func(t *testing.T) {
			_, err := exec(actor, nil, insertTeam, team)
			wantErr(t, "create", err, "row-level security")

			if n, err := exec(actor, withTeam, update, team); err != nil || n != 0 {
				t.Errorf("update = %d, %v; want 0 rows", n, err)
			}
			if n, err := exec(actor, withTeam, remove, team); err != nil || n != 0 {
				t.Errorf("delete = %d, %v; want 0 rows", n, err)
			}

			var teamID int
			withTeamID := func(tx *sql.Tx) {
				if err := tx.QueryRow(insertTeam+` RETURNING id`, team).Scan(&teamID); err != nil {
					t.Fatalf("creating the Team failed: %v", err)
				}
			}
			err = asUser(t, db, actor, withTeamID, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, teamID, actor)
				return err
			})
			wantErr(t, "add member", err, "row-level security")
		})
	}
}

// Group policies are written with global group_policy:* only, and
// a policy on an OrgUnit covers its subtree.
func TestGroupPolicy_NotBySubtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	role := "d82-policy-writer-" + o.suffix

	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES ('v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		        ROW(NULL, ARRAY['group_policy:read', 'group_policy:create', 'group_policy:update', 'group_policy:delete']::api.permission_action[])::api.role_spec)`, role)

	// ua: the writer role on subtree a.
	o.bind(t, db, "ua", role, "a")

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).role = $1`, role)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = $1`, role)
	})

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatalf("reading the admin user failed: %v", err)
	}

	unit := o.unit("a1")
	insert := `INSERT INTO api.group_policies (api_version, kind, metadata, spec)
		VALUES ('v1', 'GroupPolicy', ROW(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
		        ROW('OrgUnit', $1, '[{"model": "a"}]'::jsonb, NULL, NULL)::api.group_policy_spec)`
	withPolicy := func(tx *sql.Tx) { mustTxExec(t, tx, insert, unit) }
	update := `UPDATE api.group_policies SET spec.denied_models = '[{"model": "b"}]'::jsonb WHERE (spec).subject_name = $1`
	remove := `DELETE FROM api.group_policies WHERE (spec).subject_name = $1`
	count := `SELECT count(*) FROM api.group_policies WHERE (spec).subject_name = $1`

	exec := func(actor string, prelude func(tx *sql.Tx), query string, args ...any) (int64, error) {
		t.Helper()

		var n int64
		err := asUser(t, db, actor, prelude, func(tx *sql.Tx) error {
			res, err := tx.Exec(query, args...)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			return nil
		})

		return n, err
	}

	read := func(actor string) int {
		t.Helper()

		var n int
		if err := asUser(t, db, actor, withPolicy, func(tx *sql.Tx) error {
			return tx.QueryRow(count, unit).Scan(&n)
		}); err != nil {
			t.Fatalf("read failed: %v", err)
		}

		return n
	}

	t.Run("a global writer writes", func(t *testing.T) {
		if n, err := exec(admin, nil, insert, unit); err != nil || n != 1 {
			t.Errorf("create = %d, %v", n, err)
		}
		if n, err := exec(admin, withPolicy, update, unit); err != nil || n != 1 {
			t.Errorf("update = %d, %v", n, err)
		}
		if n, err := exec(admin, withPolicy, remove, unit); err != nil || n != 1 {
			t.Errorf("delete = %d, %v", n, err)
		}
		if n := read(admin); n != 1 {
			t.Errorf("read = %d rows", n)
		}
	})

	for _, who := range []string{"ua"} {
		actor := o.users[who]

		t.Run(who+" reads and writes nothing", func(t *testing.T) {
			_, err := exec(actor, nil, insert, unit)
			wantErr(t, "create", err, "row-level security")

			if n, err := exec(actor, withPolicy, update, unit); err != nil || n != 0 {
				t.Errorf("update = %d, %v; want 0 rows", n, err)
			}
			if n, err := exec(actor, withPolicy, remove, unit); err != nil || n != 0 {
				t.Errorf("delete = %d, %v; want 0 rows", n, err)
			}
			if n := read(actor); n != 0 {
				t.Errorf("read = %d rows; want 0", n)
			}
		})
	}
}

func TestGroupPolicy_OrgUnitSubtreeAndInactive(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	policy := func(unit, allowed string) {
		mustExec(t, db, `
			INSERT INTO api.group_policies (api_version, kind, metadata, spec)
			VALUES ('v1', 'GroupPolicy', ROW(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW('OrgUnit', $1, $2::jsonb, NULL, NULL)::api.group_policy_spec)`, o.unit(unit), allowed)
	}

	allowed := func(user string) string {
		t.Helper()

		var out sql.NullString
		if err := db.QueryRow(`SELECT NULLIF(api.get_api_key_model_rules($1::uuid, 'default', NULL) -> 'allowed_models', 'null'::jsonb)::text`,
			o.users[user]).Scan(&out); err != nil {
			t.Fatalf("get_api_key_model_rules failed: %v", err)
		}

		return out.String
	}

	policy("root", `[{"model": "a"}, {"model": "b"}]`)
	policy("a", `[{"model": "b"}, {"model": "c"}]`)

	cases := map[string]string{
		"ua1":    `[{"model": "b"}]`, // a1 under a under root
		"ux":     `[{"model": "b"}]`, // x is Inactive: its path still constrains
		"ub":     `[{"model": "a"}, {"model": "b"}]`,
		"nobody": "",
	}

	for user, want := range cases {
		if got := allowed(user); got != want {
			t.Errorf("%s: allowed = %q, want %q", user, got, want)
		}
	}
}

// An OrgUnit pool covers its subtree, Inactive units included; pools are read
// with global group_policy:read only.
func TestGroupTokenPool_Subtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	role := "d82-pool-reader-" + o.suffix

	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES ('v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		        ROW(NULL, ARRAY['group_policy:read']::api.permission_action[])::api.role_spec)`, role)

	// ua: the reader role on subtree a.
	o.bind(t, db, "ua", role, "a")

	mustExec(t, db, `
		INSERT INTO api.group_policies (api_version, kind, metadata, spec)
		VALUES ('v1', 'GroupPolicy', ROW(NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
		        ROW('OrgUnit', $1, NULL, NULL, '{"limit": 100}'::jsonb)::api.group_policy_spec)`, o.unit("a"))

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.group_policies WHERE (spec).subject_name = $1`, o.unit("a"))
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).role = $1`, role)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = $1`, role)
	})

	t.Run("an OrgUnit pool covers its subtree", func(t *testing.T) {
		for user, want := range map[string]bool{"ua": true, "ua1": true, "ux": true, "ub": false, "nobody": false} {
			var got bool
			if err := db.QueryRow(`SELECT (api.get_api_key_model_rules($1::uuid, 'default', NULL) ->> 'group_token_pool')::boolean`,
				o.users[user]).Scan(&got); err != nil {
				t.Fatal(err)
			}

			if got != want {
				t.Errorf("%s: group_token_pool %v, want %v", user, got, want)
			}
		}
	})

	t.Run("pools are read with global group_policy:read only", func(t *testing.T) {
		var admin string
		if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
			t.Fatal(err)
		}

		query := `SELECT count(*) FROM api.get_group_token_pools('OrgUnit', $1)`

		var n int
		if err := asUser(t, db, admin, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(query, o.unit("a")).Scan(&n)
		}); err != nil || n != 1 {
			t.Errorf("global admin: %d rows, %v", n, err)
		}

		for _, who := range []string{"ua"} {
			err := asUser(t, db, o.users[who], nil, func(tx *sql.Tx) error {
				return tx.QueryRow(query, o.unit("a")).Scan(&n)
			})
			wantErr(t, who, err, "global group_policy:read is required")
		}
	})
}

// A subtree binding of workspace:usage-read does not read usage by group.
func TestGroupUsage_UsageReadNotBySubtree(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)

	role := "d82-usage-reader-" + o.suffix

	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES ('v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		        ROW(NULL, ARRAY['workspace:read', 'workspace:usage-read']::api.permission_action[])::api.role_spec)`, role)

	// ua: the reader role on subtree a.
	o.bind(t, db, "ua", role, "a")

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).role = $1`, role)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = $1`, role)
	})

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatalf("reading the admin user failed: %v", err)
	}

	query := `SELECT count(*) FROM api.get_usage_by_group(CURRENT_DATE - 1, CURRENT_DATE, 'OrgUnit', NULL, $1)`

	if err := asUser(t, db, admin, nil, func(tx *sql.Tx) error {
		var n int
		return tx.QueryRow(query, "default").Scan(&n)
	}); err != nil {
		t.Errorf("global admin: %v", err)
	}

	for _, who := range []string{"ua"} {
		err := asUser(t, db, o.users[who], nil, func(tx *sql.Tx) error {
			var n int
			return tx.QueryRow(query, "default").Scan(&n)
		})
		wantErr(t, who, err, "global workspace:usage-read is required")
	}
}

// p_org_unit of get_usage_by_dimension is the OrgUnit and its subtree, as
// in get_usage_by_group, with the same totals.
func TestSubtreeRead_UsageByDimension(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	ws := "ud-subtree-ws-" + o.suffix

	keys := map[string]string{}
	for _, u := range []string{"ua", "ua1", "ux", "ub"} {
		keys[u] = createTestAPIKey(t, o.users[u], ws, 0)
	}

	// ua's 5 tokens were recorded while ua was in b.
	usage := func(tx *sql.Tx) {
		for _, r := range []struct {
			user, unit string
			tokens     int
		}{{"ua", "a", 10}, {"ua", "b", 5}, {"ua1", "a1", 20}, {"ux", "x", 40}, {"ub", "b", 80}} {
			mustTxExec(t, tx, `
				INSERT INTO api.group_daily_usage (usage_date, user_id, org_unit_id, workspace,
				    endpoint_type, endpoint_name, model_name, requests, total_tokens, api_key_id)
				VALUES (CURRENT_DATE, $1, $2, $3, 'endpoint', 'ep', 'm', 1, $4, $5)`,
				o.users[r.user], o.units[r.unit], ws, r.tokens, keys[r.user])
		}
	}

	byGroup := func(who, unit string) int64 {
		var n int64
		if err := asUser(t, db, who, usage, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COALESCE(SUM(usage), 0) FROM api.get_usage_by_group(
			    '1970-01-01', '2999-12-31', 'OrgUnit', $1, $2)`, unit, ws).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	dadmin := o.users["dadmin"]

	sum := func(m map[string]int64) (n int64) {
		for _, v := range m {
			n += v
		}
		return n
	}

	t.Run("department admin without a filter: keys recorded in its subtree", func(t *testing.T) {
		got := dimensionUsage(t, db, dadmin, usage, ws, nil, nil)
		if len(got) != 2 || got[keys["ua"]] != 10 || got[keys["ua1"]] != 20 {
			t.Errorf("got %v", got)
		}
	})

	for _, c := range []struct {
		name, who, unit string
		want            int64
	}{
		{"department admin, its OrgUnit", dadmin, o.unit("a"), 30},
		{"department admin, a child", dadmin, o.unit("a1"), 20},
		{"department admin, outside its subtree", dadmin, o.unit("b"), 0},
		{"global admin, a subtree", admin, o.unit("a"), 70},
		{"global admin, the root", admin, o.unit("root"), 155},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := sum(dimensionUsage(t, db, c.who, usage, ws, nil, c.unit))
			if got != c.want {
				t.Errorf("p_org_unit total %d, want %d", got, c.want)
			}

			if c.want > 0 {
				if g := byGroup(c.who, c.unit); g != got {
					t.Errorf("p_org_unit total %d, usage by group %d", got, g)
				}
			}
		})
	}

	t.Run("an owner reads its own key under any OrgUnit", func(t *testing.T) {
		got := dimensionUsage(t, db, o.users["ua"], usage, ws, nil, o.unit("b"))
		if len(got) != 1 || got[keys["ua"]] != 5 {
			t.Errorf("got %v", got)
		}
	})
}

// NEU-904: p_direct reads an OrgUnit without its children, or, with no
// OrgUnit, the usage recorded under none or under a deleted one.
func TestUsageByGroup_Direct(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	ws := "usage-direct-ws-" + o.suffix

	usage := func(tx *sql.Tx) {
		for _, r := range []struct {
			user, unit string
			tokens     int
		}{{"ua", "a", 10}, {"ua1", "a1", 20}, {"ub", "b", 80}, {"nobody", "", 7}} {
			var unit any
			if r.unit != "" {
				unit = o.units[r.unit]
			}
			mustTxExec(t, tx, `
				INSERT INTO api.group_daily_usage (usage_date, user_id, org_unit_id, workspace,
				    endpoint_type, endpoint_name, model_name, requests, total_tokens)
				VALUES (CURRENT_DATE, $1, $2, $3, 'endpoint', 'ep', 'm', 1, $4)`,
				o.users[r.user], unit, ws, r.tokens)
		}

		// ux's 3 tokens were recorded under a leaf OrgUnit deleted since.
		var gone int
		if err := tx.QueryRow(`
			INSERT INTO api.org_units (api_version, kind, metadata, spec, path)
			VALUES ('v1', 'OrgUnit', ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW($2, $3, $4)::api.org_unit_spec, '/0/')
			RETURNING id`, o.unit("gone"), o.source, "ext-"+o.unit("gone"), o.unit("b")).Scan(&gone); err != nil {
			t.Fatal(err)
		}
		mustTxExec(t, tx, `
			INSERT INTO api.group_daily_usage (usage_date, user_id, org_unit_id, workspace,
			    endpoint_type, endpoint_name, model_name, requests, total_tokens)
			VALUES (CURRENT_DATE, $1, $2, $3, 'endpoint', 'ep', 'm', 1, 3)`, o.users["ux"], gone, ws)
		mustTxExec(t, tx, `DELETE FROM api.org_units WHERE id = $1`, gone)
	}

	totals := func(who, kind string, orgUnit any) (map[string]int, error) {
		out := map[string]int{}
		err := asUser(t, db, who, usage, func(tx *sql.Tx) error {
			rows, err := tx.Query(`
				SELECT name, usage FROM api.get_usage_by_group(
				    p_start_date => CURRENT_DATE, p_end_date => CURRENT_DATE, p_kind => $1,
				    p_workspace => $2, p_org_unit => $3, p_direct => TRUE)`, kind, ws, orgUnit)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var name string
				var n int
				if err := rows.Scan(&name, &n); err != nil {
					return err
				}
				out[name] += n
			}

			return rows.Err()
		})

		return out, err
	}

	var admin string
	if err := db.QueryRow(`SELECT (spec).user_id FROM api.role_assignments WHERE (metadata).name = 'admin-global-role-assignment'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	dadmin := o.users["dadmin"]
	u := o.users

	for _, c := range []struct {
		name, who, kind string
		orgUnit         any
		want            map[string]int
	}{
		{"global admin, users of an OrgUnit itself", admin, "User", o.unit("a"), map[string]int{u["ua"]: 10}},
		{"global admin, an OrgUnit itself", admin, "OrgUnit", o.unit("a"), map[string]int{o.unit("a"): 10}},
		{"department admin, its OrgUnit itself", dadmin, "OrgUnit", o.unit("a"), map[string]int{o.unit("a"): 10}},
		{"department admin, outside its subtree", dadmin, "User", o.unit("b"), map[string]int{}},
		{"global admin, users in no OrgUnit or a deleted one", admin, "User", nil, map[string]int{u["nobody"]: 7, u["ux"]: 3}},
		{"global admin, the parent of a deleted OrgUnit itself", admin, "OrgUnit", o.unit("b"), map[string]int{o.unit("b"): 80}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := totals(c.who, c.kind, c.orgUnit)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}

	t.Run("department admin, users in no OrgUnit: refused", func(t *testing.T) {
		_, err := totals(dadmin, "User", nil)
		wantErr(t, "dadmin unassigned", err, "global workspace:usage-read is required")
	})
}
