package dbtest

import (
	"database/sql"
	"strings"
	"testing"
)

// Tests for the team-admin role assigned on one Team: what it may do on that
// Team, and what it may not do anywhere.

// syncedTeam stores a Team as directory sync would (write checks skipped)
// and returns its id.
func syncedTeam(t *testing.T, db *sql.DB, name string) int {
	t.Helper()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)

	var id int
	if err := tx.QueryRow(`
		INSERT INTO api.teams (api_version, kind, metadata, spec, status)
		VALUES ('v1', 'Team', ROW($1, $1, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		        ROW('ta-src', 'ext-' || $1)::api.team_spec, ROW('Active', now())::api.org_status)
		RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("creating synced Team %s failed: %v", name, err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).team = $1 OR (spec).subject_name = $1`, name)
		_, _ = db.Exec(`DELETE FROM api.teams WHERE id = $1`, id)
	})

	return id
}

type teamAdminFixture struct {
	db                  *sql.DB
	team, other, synced string
	teamID, otherID     int
	syncedID            int
	admin, member       string
	outsider            string
}

func newTeamAdminFixture(t *testing.T) *teamAdminFixture {
	t.Helper()

	db := GetTestDB(t)
	f := &teamAdminFixture{db: db}

	f.team = newOrgName("ta-team")
	f.other = newOrgName("ta-other")
	f.synced = newOrgName("ta-synced")
	f.teamID = manualTeam(t, db, f.team)
	f.otherID = manualTeam(t, db, f.other)
	f.syncedID = syncedTeam(t, db, f.synced)

	f.admin = rawUser(t, db, "ta-admin")
	f.member = rawUser(t, db, "ta-member")
	f.outsider = rawUser(t, db, "ta-outsider")

	bindTeam(t, db, f.admin, "team-admin", f.team)
	bindTeam(t, db, f.admin, "team-admin", f.synced)
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.teamID, f.member)
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.otherID, f.member)

	return f
}

func (f *teamAdminFixture) exec(t *testing.T, query string, args ...any) (int64, error) {
	t.Helper()

	var n int64

	err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
		res, err := tx.Exec(query, args...)
		if err != nil {
			return err
		}

		n, err = res.RowsAffected()

		return err
	})

	return n, err
}

func TestTeamAdmin_TeamAndMembers(t *testing.T) {
	f := newTeamAdminFixture(t)

	t.Run("reads its Teams only", func(t *testing.T) {
		if n := countAs(t, f.db, f.admin, nil, `SELECT count(*) FROM api.teams WHERE id IN ($1, $2)`, f.teamID, f.otherID); n != 1 {
			t.Fatalf("sees %d of its Team and another, want 1", n)
		}

		if n := countAs(t, f.db, f.admin, nil, `SELECT count(*) FROM api.team_members WHERE user_id = $1`, f.member); n != 1 {
			t.Fatalf("sees %d memberships of a member of two Teams, want 1", n)
		}
	})

	t.Run("adds and removes members of its Team", func(t *testing.T) {
		if _, err := f.exec(t, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.teamID, f.outsider); err != nil {
			t.Fatalf("adding a member failed: %v", err)
		}

		if n, err := f.exec(t, `DELETE FROM api.team_members WHERE team_id = $1 AND user_id = $2`, f.teamID, f.member); err != nil || n != 1 {
			t.Fatalf("removing a member: %d rows, %v", n, err)
		}
	})

	t.Run("cannot add members to another Team", func(t *testing.T) {
		_, err := f.exec(t, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.otherID, f.outsider)
		wantErr(t, "adding to another Team", err, "row-level security")

		if n, err := f.exec(t, `DELETE FROM api.team_members WHERE team_id = $1`, f.otherID); err != nil || n != 0 {
			t.Fatalf("removing from another Team: %d rows, %v", n, err)
		}
	})

	t.Run("members of a synced Team stay read-only", func(t *testing.T) {
		_, err := f.exec(t, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.syncedID, f.outsider)
		wantErr(t, "adding to a synced Team", err, "10290")
	})

	t.Run("cannot add a member who would gain a role assignment", func(t *testing.T) {
		assignGroup(t, "Team", f.team, "workspace-user")

		_, err := f.exec(t, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.teamID, f.outsider)
		wantErr(t, "adding to a Team with a role assignment", err, "10296")
	})
}

// team-admin does not reach policies, token pools, roles, Teams themselves
// or users.
func TestTeamAdmin_NoEscalation(t *testing.T) {
	f := newTeamAdminFixture(t)

	cases := []struct {
		name  string
		query string
		args  []any
		want  string
	}{
		{"set the Team's group policy", `
			INSERT INTO api.group_policies (api_version, kind, metadata, spec)
			VALUES ('v1', 'GroupPolicy', ROW('ta-gp', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW('Team', $1, NULL, NULL, '{"limit": 1000000}'::jsonb)::api.group_policy_spec)`, []any{f.team}, "row-level security"},
		{"grant itself a role", `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment', ROW('ta-ra', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW($1::uuid, NULL, TRUE, 'admin', NULL, 'User', NULL, NULL)::api.role_assignment_spec)`, []any{f.admin}, "10283"},
		{"grant a member team-admin on the Team", `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment', ROW('ta-ra2', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW($1::uuid, NULL, FALSE, 'team-admin', NULL, 'User', NULL, $2)::api.role_assignment_spec)`, []any{f.member, f.team}, "10314"},
	}

	for _, tc := range cases {
		t.Run("cannot "+tc.name, func(t *testing.T) {
			_, err := f.exec(t, tc.query, tc.args...)
			wantErr(t, tc.name, err, tc.want)
		})
	}

	t.Run("cannot rename or delete the Team", func(t *testing.T) {
		if n, err := f.exec(t, `UPDATE api.teams SET metadata.display_name = 'x' WHERE id = $1`, f.teamID); err != nil || n != 0 {
			t.Fatalf("update: %d rows, %v", n, err)
		}

		if n, err := f.exec(t, `DELETE FROM api.teams WHERE id = $1`, f.teamID); err != nil || n != 0 {
			t.Fatalf("delete: %d rows, %v", n, err)
		}
	})

	t.Run("holds no permission on users, roles or policies", func(t *testing.T) {
		var perms string
		if err := f.db.QueryRow(`SELECT (spec).permissions::text FROM api.roles WHERE (metadata).name = 'team-admin'`).Scan(&perms); err != nil {
			t.Fatal(err)
		}

		for _, p := range []string{"user_profile:", "role:", "role_assignment:", "group_policy:", "team:create", "team:update", "team:delete"} {
			if strings.Contains(perms, p) {
				t.Errorf("team-admin holds %s*: %s", p, perms)
			}
		}

		if hasPermission(t, f.admin, "service_account:create", "") {
			t.Error("a Team-scoped team-admin holds service_account:create globally")
		}
	})
}

func TestTeamAdmin_UsageByGroup(t *testing.T) {
	f := newTeamAdminFixture(t)

	call := func(kind string, name, team any) error {
		return asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`SELECT * FROM api.get_usage_by_group('1970-01-01', '2999-12-31', $1, $2, NULL, NULL, $3)`, kind, name, team)
			return err
		})
	}

	for _, tc := range []struct {
		name       string
		kind       string
		pName, pTm any
		allowed    bool
	}{
		{"its Team by name", "Team", f.team, nil, true},
		{"its Team by p_team", "Team", nil, f.team, true},
		{"the users of its Team", "User", nil, f.team, true},
		{"another Team", "Team", f.other, nil, false},
		{"the users of another Team", "User", nil, f.other, false},
		{"every Team", "Team", nil, nil, false},
		{"its Team by OrgUnit", "OrgUnit", nil, f.team, false},
		{"every user", "User", nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := call(tc.kind, tc.pName, tc.pTm)
			if tc.allowed && err != nil {
				t.Fatalf("refused: %v", err)
			}

			if !tc.allowed {
				wantErr(t, tc.name, err, "usage-read is required")
			}
		})
	}
}

// team-admin reads its own Team's token pool and no other.
func TestTeamAdmin_ReadsPool(t *testing.T) {
	f := newTeamAdminFixture(t)
	pool := setPool(t, "Team", f.team, 100, "monthly")
	setPool(t, "Team", f.other, 50, "monthly")

	recordUsage(t, createTestAPIKey(t, f.member, "ta-ws", 0), "m1", 30, 10, nil)
	aggregateUsage(t)

	read := func(user string, kind, name any) (int, int64, error) {
		var (
			n    int
			left int64
		)

		err := asUser(t, f.db, user, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT count(*), COALESCE(sum(remaining), 0) FROM api.get_group_token_pools($1, $2)`, kind, name).Scan(&n, &left)
		})

		return n, left, err
	}

	if n, left, err := read(f.admin, "Team", f.team); err != nil || n != 1 || left != 60 {
		t.Fatalf("team-admin, own Team: %d pools, remaining %d, %v; want 1, 60", n, left, err)
	}

	for _, c := range []struct {
		what       string
		user       string
		kind, name any
	}{
		{"team-admin, another Team", f.admin, "Team", f.other},
		{"team-admin, every pool", f.admin, nil, nil},
		{"team-admin, every Team pool", f.admin, "Team", nil},
		{"a member", f.member, "Team", f.team},
	} {
		_, _, err := read(c.user, c.kind, c.name)
		wantErr(t, c.what, err, "group_policy:read is required")
	}

	// Writing the pool still needs global group_policy:update; RLS hides the row.
	if n := countAs(t, f.db, f.admin, nil, `
		WITH u AS (
			UPDATE api.group_policies SET spec.token_quota = '{"limit": 1000000, "period": "monthly"}'::jsonb
			WHERE (metadata).name = $1 RETURNING 1)
		SELECT count(*) FROM u`, pool); n != 0 {
		t.Fatalf("team-admin updated %d pools", n)
	}
}

func TestTeamAdmin_ReadsUserProfiles(t *testing.T) {
	f := newTeamAdminFixture(t)
	profiles := `SELECT count(*) FROM api.user_profiles WHERE id = $1`

	t.Run("team-admin reads its members and the users it may add", func(t *testing.T) {
		for name, id := range map[string]string{"member": f.member, "outsider": f.outsider} {
			if n := countAs(t, f.db, f.admin, nil, profiles, id); n != 1 {
				t.Errorf("team-admin sees %d profiles of the %s, want 1", n, name)
			}
		}
	})

	t.Run("team:read alone reads the members only", func(t *testing.T) {
		reader := rawUser(t, f.db, "ta-reader")
		bindTeam(t, f.db, reader, customRole(t, f.db, "team:read"), f.team)

		if n := countAs(t, f.db, reader, nil, profiles, f.member); n != 1 {
			t.Errorf("a Team reader sees %d profiles of a member, want 1", n)
		}

		if n := countAs(t, f.db, reader, nil, profiles, f.outsider); n != 0 {
			t.Errorf("a Team reader sees %d profiles of a non-member, want 0", n)
		}
	})

	t.Run("a member reads no other profile", func(t *testing.T) {
		if n := countAs(t, f.db, f.member, nil, `SELECT count(*) FROM api.user_profiles WHERE id <> $1`, f.member); n != 0 {
			t.Errorf("a plain member sees %d other profiles, want 0", n)
		}
	})
}

// p_team reads a Team's usage by key: the same total as the Team's usage by
// group, without the caller's own usage outside the Team.
func TestTeamAdmin_UsageByDimensionTeam(t *testing.T) {
	f := newTeamAdminFixture(t)
	const ws = "ta-dim-ws"

	memberKey := createTestAPIKey(t, f.member, ws, 0)
	adminKey := createTestAPIKey(t, f.admin, ws, 0)
	outsiderKey := createTestAPIKey(t, f.outsider, ws, 0)

	recordUsage(t, memberKey, "m1", 10, 5, nil)
	recordUsage(t, memberKey, "m2", 7, 0, nil)
	recordUsage(t, adminKey, "m1", 100, 0, nil)
	recordUsage(t, outsiderKey, "m1", 1000, 0, nil)
	aggregateUsage(t)

	got := dimensionUsage(t, f.db, f.admin, nil, ws, f.team, nil)
	if len(got) != 1 || got[memberKey] != 22 {
		t.Fatalf("p_team: %v, want the member's key with 22", got)
	}

	var byGroup int64
	if err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
		return tx.QueryRow(`
			SELECT COALESCE(SUM(usage), 0) FROM api.get_usage_by_group(
			    p_start_date => '1970-01-01', p_end_date => '2999-12-31', p_kind => 'Team',
			    p_name => $1, p_workspace => $2)`, f.team, ws).Scan(&byGroup)
	}); err != nil {
		t.Fatal(err)
	}

	if byGroup != got[memberKey] {
		t.Errorf("p_team total %d, usage by group %d", got[memberKey], byGroup)
	}

	if all := dimensionUsage(t, f.db, f.admin, nil, ws, nil, nil); len(all) != 2 || all[adminKey] != 100 || all[memberKey] != 22 {
		t.Errorf("no filter: %v, want own key and the member's key", all)
	}
}
