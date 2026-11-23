package dbtest

import (
	"database/sql"
	"testing"
)

// Tests for api.api_key_owner_active: a personal API key stops working when
// its owner is deactivated.

func ownerActive(t *testing.T, tx *sql.Tx, keyID string) bool {
	t.Helper()

	var active bool
	if err := tx.QueryRow(`SELECT api.api_key_owner_active($1)`, keyID).Scan(&active); err != nil {
		t.Fatalf("api_key_owner_active failed: %v", err)
	}

	return active
}

func TestAPIKeyOwnerActive(t *testing.T) {
	db := GetTestDB(t)
	owner := rawUser(t, db, "oa-owner")
	key := createTestAPIKey(t, owner, "oa-ws", 0)

	cases := []struct {
		name    string
		prelude string
		want    bool
	}{
		{"an active owner", ``, true},
		{"deactivated by the organization sync",
			`INSERT INTO api.external_identities (source, external_id, user_id, sync_deactivated)
			 VALUES ('ldap:oa', 'oa-' || $1::text, $1::uuid, 'banned')`, false},
		{"linked, not deactivated",
			`INSERT INTO api.external_identities (source, external_id, user_id) VALUES ('ldap:oa', 'oa-' || $1::text, $1::uuid)`, true},
		{"banned in GoTrue", `UPDATE auth.users SET banned_until = now() + interval '1 day' WHERE id = $1`, false},
		{"a ban that has passed", `UPDATE auth.users SET banned_until = now() - interval '1 day' WHERE id = $1`, true},
		{"being deleted",
			`UPDATE api.user_profiles SET metadata.deletion_timestamp = now() WHERE id = $1`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()

			if tc.prelude != "" {
				mustTxExec(t, tx, `SET LOCAL session_replication_role = replica`)
				mustTxExec(t, tx, tc.prelude, owner)
			}

			if got := ownerActive(t, tx, key); got != tc.want {
				t.Fatalf("api_key_owner_active = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("callers other than service_role may not ask", func(t *testing.T) {
		err := asUser(t, db, owner, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`SELECT api.api_key_owner_active($1)`, key)
			return err
		})
		wantErr(t, "api_user", err, "permission denied")
	})
}
