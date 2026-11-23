package dbtest

import (
	"database/sql"
	"testing"
)

// Tests for ServiceAccounts and their API keys: managed with
// service_account:* on the SA's Team, independent of the user who created
// them.

// saWorkspace is the seeded workspace; SA keys need an existing one.
const saWorkspace = "default"

type saFixture struct {
	db                    *sql.DB
	team, other           string
	teamID, otherID       int
	admin, member, nobody string
}

func newSAFixture(t *testing.T) *saFixture {
	t.Helper()

	db := GetTestDB(t)
	f := &saFixture{db: db}
	f.team = newOrgName("sa-team")
	f.other = newOrgName("sa-other")
	f.teamID = manualTeam(t, db, f.team)
	f.otherID = manualTeam(t, db, f.other)

	f.admin = rawUser(t, db, "sa-admin")
	f.member = rawUser(t, db, "sa-member")
	f.nobody = rawUser(t, db, "sa-nobody")

	bindTeam(t, db, f.admin, "team-admin", f.team)
	mustExec(t, db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.teamID, f.member)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).team IN ($1, $2)`, f.team, f.other)
	})

	return f
}

// commitAs runs fn as userID under RLS and commits.
func commitAs(t *testing.T, db *sql.DB, userID string, fn func(tx *sql.Tx) error) error {
	t.Helper()

	return asUser(t, db, userID, nil, func(tx *sql.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}

		return tx.Commit()
	})
}

// createSA creates a ServiceAccount of teamID as actor and commits it.
func createSA(t *testing.T, db *sql.DB, actor string, teamID int) (string, error) {
	t.Helper()

	var id string

	err := commitAs(t, db, actor, func(tx *sql.Tx) error {
		return tx.QueryRow(`
			INSERT INTO api.service_accounts (metadata, spec, team_id)
			VALUES (ROW($1, NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata,
			        ROW('for tests', FALSE)::api.service_account_spec, $2)
			RETURNING id`, newOrgName("sa"), teamID).Scan(&id)
	})
	if err != nil {
		return "", err
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.api_daily_usage WHERE (spec).api_key_id IN (SELECT id FROM api.api_keys WHERE service_account_id = $1)`, id)
		_, _ = db.Exec(`DELETE FROM api.api_keys WHERE service_account_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM api.group_daily_usage WHERE service_account_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM api.service_accounts WHERE id = $1`, id)
	})

	return id, nil
}

func (f *saFixture) mustSA(t *testing.T) string {
	t.Helper()

	id, err := createSA(t, f.db, f.admin, f.teamID)
	if err != nil {
		t.Fatalf("creating a ServiceAccount failed: %v", err)
	}

	return id
}

// createSAKey creates a key of the SA as actor and commits it.
func createSAKey(t *testing.T, db *sql.DB, actor string, saID any, workspace string, limits any) (string, error) {
	t.Helper()

	var id string

	if limits != nil {
		limits = mustJSON(limits)
	}

	err := commitAs(t, db, actor, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM api.create_service_account_api_key(p_service_account_id := $1, p_workspace := $2, p_name := $3, p_limits := $4::jsonb)`,
			saID, workspace, newOrgName("sa-key"), limits).Scan(&id)
	})

	return id, err
}

func (f *saFixture) mustSAKey(t *testing.T, saID string, limits any) string {
	t.Helper()

	id, err := createSAKey(t, f.db, f.admin, saID, saWorkspace, limits)
	if err != nil {
		t.Fatalf("creating a ServiceAccount key failed: %v", err)
	}

	return id
}

func TestServiceAccount_Manage(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)

	var createdBy string
	if err := f.db.QueryRow(`SELECT created_by FROM api.service_accounts WHERE id = $1`, sa).Scan(&createdBy); err != nil || createdBy != f.admin {
		t.Fatalf("created_by = %s, %v", createdBy, err)
	}

	for _, tc := range []struct {
		name   string
		actor  string
		teamID int
	}{
		{"a member of the Team", f.member, f.teamID},
		{"a user without service_account:create", f.nobody, f.teamID},
		{"team-admin of another Team", f.admin, f.otherID},
	} {
		t.Run(tc.name+" cannot create one", func(t *testing.T) {
			_, err := createSA(t, f.db, tc.actor, tc.teamID)
			wantErr(t, tc.name, err, "row-level security")
		})
	}

	t.Run("visible to team-admin only", func(t *testing.T) {
		for name, tc := range map[string]struct {
			user string
			want int
		}{
			"team-admin":   {f.admin, 1},
			"a member":     {f.member, 0},
			"another user": {f.nobody, 0},
		} {
			if n := countAs(t, f.db, tc.user, nil, `SELECT count(*) FROM api.service_accounts WHERE id = $1`, sa); n != tc.want {
				t.Errorf("%s sees %d, want %d", name, n, tc.want)
			}
		}
	})

	t.Run("not a user and never a member", func(t *testing.T) {
		if n := countAs(t, f.db, f.admin, nil, `SELECT count(*) FROM api.user_profiles WHERE id = $1`, sa); n != 0 {
			t.Errorf("the SA is listed among users")
		}

		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM api.user_profiles WHERE id = $1`, sa).Scan(&n); err != nil || n != 0 {
			t.Errorf("the SA has a user profile: %d, %v", n, err)
		}

		_, err := f.db.Exec(`INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.teamID, sa)
		wantErr(t, "adding the SA to a Team", err, "foreign key")
	})

	t.Run("its Team never changes", func(t *testing.T) {
		err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE api.service_accounts SET team_id = $2 WHERE id = $1`, sa, f.otherID)
			return err
		})
		wantErr(t, "moving the SA to another Team", err, "10318")
	})

	t.Run("team-admin disables it", func(t *testing.T) {
		if n := countAs(t, f.db, f.admin, nil, `
			WITH u AS (UPDATE api.service_accounts SET spec.disabled = TRUE WHERE id = $1 RETURNING 1)
			SELECT count(*) FROM u`, sa); n != 1 {
			t.Fatalf("team-admin disabled %d SAs", n)
		}

		if n := countAs(t, f.db, f.member, nil, `
			WITH u AS (UPDATE api.service_accounts SET spec.disabled = TRUE WHERE id = $1 RETURNING 1)
			SELECT count(*) FROM u`, sa); n != 0 {
			t.Fatalf("a member disabled %d SAs", n)
		}
	})

	t.Run("an Inactive Team gets no new SA", func(t *testing.T) {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
		mustTxExec(t, tx, `UPDATE api.teams SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, f.otherID)
		mustTxExec(t, tx, `SET LOCAL session_replication_role = DEFAULT`)

		_, err = tx.Exec(`INSERT INTO api.service_accounts (metadata, team_id)
			VALUES (ROW('sa-inactive', NULL, NULL, NULL, NULL, NULL, NULL, NULL)::api.metadata, $1)`, f.otherID)
		wantErr(t, "an Inactive Team", err, "10317")
	})
}

func TestServiceAccount_Keys(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	key := f.mustSAKey(t, sa, nil)

	var (
		userID    sql.NullString
		owner     string
		createdBy string
	)
	if err := f.db.QueryRow(`SELECT user_id, service_account_id, created_by FROM api.api_keys WHERE id = $1`, key).
		Scan(&userID, &owner, &createdBy); err != nil {
		t.Fatal(err)
	}

	if userID.Valid || owner != sa || createdBy != f.admin {
		t.Fatalf("stored user_id=%v service_account_id=%s created_by=%s", userID, owner, createdBy)
	}

	for _, tc := range []struct {
		name      string
		actor     string
		sa        any
		workspace string
		want      string
	}{
		{"a member of the Team", f.member, sa, saWorkspace, "service_account:create on the Team of the ServiceAccount is required"},
		{"a user without service_account:create", f.nobody, sa, saWorkspace, "service_account:create on the Team of the ServiceAccount is required"},
		{"no SA", f.admin, nil, saWorkspace, "p_service_account_id is required"},
		{"an unknown workspace", f.admin, sa, "sa-no-such-ws", "10044"},
		{"the SA itself", sa, sa, saWorkspace, "10320"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			_, err := createSAKey(t, f.db, tc.actor, tc.sa, tc.workspace, nil)
			wantErr(t, tc.name, err, tc.want)
		})
	}

	t.Run("the SA cannot create SAs", func(t *testing.T) {
		_, err := createSA(t, f.db, sa, f.teamID)
		wantErr(t, "an SA creating an SA", err, "10320")
	})

	t.Run("visible to team-admin only", func(t *testing.T) {
		for name, tc := range map[string]struct {
			user string
			want int
		}{
			"team-admin":   {f.admin, 1},
			"a member":     {f.member, 0},
			"another user": {f.nobody, 0},
		} {
			if n := countAs(t, f.db, tc.user, nil, `SELECT count(*) FROM api.api_keys WHERE id = $1`, key); n != tc.want {
				t.Errorf("%s sees %d, want %d", name, n, tc.want)
			}
		}
	})

	t.Run("team-admin sets limits and reads them", func(t *testing.T) {
		if err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`SELECT api.set_api_key_limits($1, '{"rpm": 10}'::jsonb)`, key); err != nil {
				return err
			}

			var limits sql.NullString
			if err := tx.QueryRow(`SELECT api.get_api_key_limits($1)::text`, key).Scan(&limits); err != nil {
				return err
			}

			if !limits.Valid {
				t.Error("get_api_key_limits returned NULL for team-admin")
			}

			return nil
		}); err != nil {
			t.Fatalf("team-admin: %v", err)
		}

		err := asUser(t, f.db, f.nobody, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`SELECT api.set_api_key_limits($1, '{"rpm": 10}'::jsonb)`, key)
			return err
		})
		wantErr(t, "another user setting limits", err, "not found or not owned")
	})

	t.Run("the owner and workspace of a key never change", func(t *testing.T) {
		err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE api.api_keys SET user_id = $2, service_account_id = NULL WHERE id = $1`, key, f.admin)
			return err
		})
		wantErr(t, "turning an SA key into a personal one", err, "10318")

		err = asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE api.api_keys SET metadata.workspace = 'elsewhere' WHERE id = $1`, key)
			return err
		})
		wantErr(t, "moving an SA key to another workspace", err, "10318")
	})

	t.Run("an SA key has no project", func(t *testing.T) {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		_, err = tx.Exec(`UPDATE api.api_keys SET spec.project_id = gen_random_uuid() WHERE id = $1`, key)
		wantErr(t, "a project on an SA key", err, "10316")
	})

	t.Run("a disabled SA gets no new key", func(t *testing.T) {
		other := f.mustSA(t)
		mustExec(t, f.db, `UPDATE api.service_accounts SET spec.disabled = TRUE WHERE id = $1`, other)

		_, err := createSAKey(t, f.db, f.admin, other, saWorkspace, nil)
		wantErr(t, "a disabled SA", err, "10317")
	})

	t.Run("team-admin soft-deletes the key", func(t *testing.T) {
		if n := countAs(t, f.db, f.admin, nil, `
			WITH u AS (UPDATE api.api_keys SET metadata.deletion_timestamp = now() WHERE id = $1 RETURNING 1)
			SELECT count(*) FROM u`, key); n != 1 {
			t.Fatalf("soft delete: %d rows", n)
		}
	})
}

// An SA key lives with its SA and Team, not with the user who created it.
// get_api_key_limits returns NULL to a caller who may not read the key's
// usage. An SA key has no user, so a caller without auth.uid() must not pass
// as its owner.
func TestServiceAccount_KeyLimitsReaders(t *testing.T) {
	f := newSAFixture(t)
	saKey := f.mustSAKey(t, f.mustSA(t), nil)

	owner := rawUser(t, f.db, "sa-key-owner")
	bindGlobal(t, f.db, owner, "workspace-user")
	userKey := createTestAPIKey(t, owner, saWorkspace, 0)

	read := func(user, key string) sql.NullString {
		t.Helper()

		var limits sql.NullString

		if user == "" {
			if err := f.db.QueryRow(`SELECT api.get_api_key_limits($1)::text`, key).Scan(&limits); err != nil {
				t.Fatalf("anonymous: %v", err)
			}

			return limits
		}

		if err := asUser(t, f.db, user, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT api.get_api_key_limits($1)::text`, key).Scan(&limits)
		}); err != nil {
			t.Fatalf("%s: %v", user, err)
		}

		return limits
	}

	for _, tc := range []struct {
		name, user, key string
		want            bool
	}{
		{"an anonymous caller, SA key", "", saKey, false},
		{"another user, SA key", f.nobody, saKey, false},
		{"a member of the Team, SA key", f.member, saKey, false},
		{"team-admin of the SA's Team, SA key", f.admin, saKey, true},
		{"the owner, own key", owner, userKey, true},
		{"an anonymous caller, user key", "", userKey, false},
	} {
		if got := read(tc.user, tc.key).Valid; got != tc.want {
			t.Errorf("%s reads limits: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestServiceAccount_KeyOwnerActive(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	key := f.mustSAKey(t, sa, nil)

	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	if !ownerActive(t, tx, key) {
		t.Fatal("a key of an enabled SA of an Active Team is not active")
	}

	mustTxExec(t, tx, `UPDATE auth.users SET banned_until = now() + interval '1 day' WHERE id = $1`, f.admin)
	mustTxExec(t, tx, `INSERT INTO api.external_identities (source, external_id, user_id, sync_deactivated)
		VALUES ('ldap:sa', 'sa-' || $1::text, $1::uuid, 'banned')`, f.admin)

	if !ownerActive(t, tx, key) {
		t.Fatal("deactivating the creator stopped the SA key")
	}

	mustTxExec(t, tx, `SAVEPOINT disabled`)
	mustTxExec(t, tx, `UPDATE api.service_accounts SET spec.disabled = TRUE WHERE id = $1`, sa)

	if ownerActive(t, tx, key) {
		t.Fatal("a key of a disabled SA is still active")
	}

	mustTxExec(t, tx, `ROLLBACK TO SAVEPOINT disabled`)
	mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
	mustTxExec(t, tx, `UPDATE api.teams SET status = ROW('Inactive', NULL)::api.org_status WHERE id = $1`, f.teamID)

	if ownerActive(t, tx, key) {
		t.Fatal("a key of an SA of an Inactive Team is still active")
	}
}

func TestServiceAccount_Delete(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	key := f.mustSAKey(t, sa, nil)

	t.Run("a Team with SAs cannot be deleted", func(t *testing.T) {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()

		// The team-admin assignment would block first.
		mustTxExec(t, tx, `DELETE FROM api.role_assignments WHERE (spec).team = $1`, f.team)

		_, err = tx.Exec(`DELETE FROM api.teams WHERE id = $1`, f.teamID)
		wantErr(t, "deleting the Team", err, "10315")
	})

	t.Run("an SA with keys cannot be deleted, a soft-deleted key included", func(t *testing.T) {
		mustExec(t, f.db, `UPDATE api.api_keys SET metadata.deletion_timestamp = now() WHERE id = $1`, key)

		err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM api.service_accounts WHERE id = $1`, sa)
			return err
		})
		wantErr(t, "deleting the SA", err, "10319")
	})

	t.Run("team-admin deletes an SA without keys", func(t *testing.T) {
		mustExec(t, f.db, `DELETE FROM api.api_keys WHERE id = $1`, key)

		if n := countAs(t, f.db, f.member, nil, `
			WITH d AS (DELETE FROM api.service_accounts WHERE id = $1 RETURNING 1)
			SELECT count(*) FROM d`, sa); n != 0 {
			t.Fatalf("a member deleted %d SAs", n)
		}

		if err := commitAs(t, f.db, f.admin, func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM api.service_accounts WHERE id = $1`, sa)
			return err
		}); err != nil {
			t.Fatalf("team-admin deleting the SA: %v", err)
		}
	})
}
