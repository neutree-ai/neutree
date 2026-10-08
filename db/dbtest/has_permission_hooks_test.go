package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

// Tests for 107_has_permission_hooks: api.has_permission is an entry point that
// calls two extension hooks (api.check_role_binding, api.check_api_key_scope).
// The community behaviour must be unchanged by the split:
//   - only global role assignments grant a permission,
//   - the workspace argument is ignored,
//   - workspace-scoped role assignments do not count,
//   - an API key request is not restricted further.

func TestHasPermissionHooksExist(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	cases := []struct {
		name string
		args string
	}{
		{"has_permission", "user_uuid uuid, required_permission api.permission_action, workspace text"},
		{"check_role_binding", "user_uuid uuid, required_permission api.permission_action, workspace text"},
		{"check_api_key_scope", "required_permission api.permission_action, workspace text"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var args string
			var secDef bool
			var volatile string
			var rettype string
			err := db.QueryRowContext(ctx, `
				SELECT pg_get_function_identity_arguments(p.oid), p.prosecdef,
				       p.provolatile::text, p.prorettype::regtype::text
				FROM pg_proc p
				JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'api' AND p.proname = $1
			`, c.name).Scan(&args, &secDef, &volatile, &rettype)
			if err != nil {
				t.Fatalf("function api.%s not found: %v", c.name, err)
			}
			if args != c.args {
				t.Errorf("api.%s arguments = %q, want %q", c.name, args, c.args)
			}
			if !secDef {
				t.Errorf("api.%s must be SECURITY DEFINER", c.name)
			}
			if volatile != "v" {
				t.Errorf("api.%s volatility = %q, want v", c.name, volatile)
			}
			if rettype != "boolean" {
				t.Errorf("api.%s returns %q, want boolean", c.name, rettype)
			}
		})
	}
}

func TestHasPermissionCommunityBehaviour(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	var globalUserID, scopedUser string

	err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		// Global assignment granting endpoint:read.
		globalUserID = createUserWithPermissions(t, tx, "hp-hooks-global", "hp-hooks-global@example.com",
			[]string{"endpoint:read"})

		// Workspace-scoped assignment granting cluster:read; community ignores it.
		// Community rejects such assignments on write (error 10041), but a row
		// may still exist (e.g. data written by the enterprise edition), so the
		// validation trigger is bypassed for this seed only.
		scopedUser = CreateTestUser(t, "hp-hooks-scoped", "hp-hooks-scoped@example.com", "password123").ID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO api.roles (api_version, kind, metadata, spec)
			VALUES ('v1', 'Role',
				ROW('hp-hooks-scoped-role', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW(NULL, ARRAY['cluster:read']::api.permission_action[])::api.role_spec)
		`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
			VALUES ('v1', 'RoleAssignment',
				ROW('hp-hooks-scoped-ra', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
				ROW($1::uuid, 'default', FALSE, 'hp-hooks-scoped-role')::api.role_assignment_spec)
		`, scopedUser)
		return err
	})
	if err != nil {
		t.Fatalf("failed to seed users: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM api.role_assignments WHERE (spec).user_id IN ($1::uuid, $2::uuid)`,
			globalUserID, scopedUser)
		_, _ = db.ExecContext(ctx, `DELETE FROM api.roles WHERE (metadata).name IN ('hp-hooks-global-role', 'hp-hooks-scoped-role')`)
	})

	// workspace is passed as SQL NULL when empty.
	check := func(t *testing.T, ctxFuncs []SetContextFunc, userID, perm, workspace string, want bool) {
		t.Helper()

		var got bool
		err := execWithContext(t, db, ctxFuncs, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT api.has_permission($1::uuid, $2::api.permission_action, NULLIF($3, ''))
			`, userID, perm, workspace).Scan(&got)
		})
		if err != nil {
			t.Fatalf("has_permission(%s, %q) failed: %v", perm, workspace, err)
		}
		if got != want {
			t.Fatalf("has_permission(%s, %q) = %v, want %v", perm, workspace, got, want)
		}
	}

	asAPIUser := func(userID string) []SetContextFunc {
		return []SetContextFunc{
			func(tx *sql.Tx) error {
				_, err := tx.Exec("SET LOCAL ROLE api_user")
				return err
			},
			setUserContext(userID),
		}
	}

	withAPIKeyClaim := func(userID string) []SetContextFunc {
		return append(asAPIUser(userID), func(tx *sql.Tx) error {
			_, err := tx.Exec(`SELECT set_config('request.jwt.claims',
				'{"api_key_id":"00000000-0000-0000-0000-000000000001"}', true)`)
			return err
		})
	}

	t.Run("global binding grants without workspace", func(t *testing.T) {
		check(t, nil, globalUserID, "endpoint:read", "", true)
	})
	t.Run("global binding ignores workspace argument", func(t *testing.T) {
		check(t, nil, globalUserID, "endpoint:read", "default", true)
		check(t, nil, globalUserID, "endpoint:read", "no-such-workspace", true)
	})
	t.Run("missing permission is denied", func(t *testing.T) {
		check(t, nil, globalUserID, "cluster:read", "", false)
		check(t, nil, globalUserID, "cluster:read", "default", false)
	})
	t.Run("workspace binding does not count", func(t *testing.T) {
		check(t, nil, scopedUser, "cluster:read", "", false)
		check(t, nil, scopedUser, "cluster:read", "default", false)
	})
	t.Run("unknown user is denied", func(t *testing.T) {
		check(t, nil, "00000000-0000-0000-0000-0000000000ff", "endpoint:read", "", false)
	})
	t.Run("same result under api_user RLS", func(t *testing.T) {
		check(t, asAPIUser(globalUserID), globalUserID, "endpoint:read", "default", true)
		check(t, asAPIUser(scopedUser), scopedUser, "cluster:read", "default", false)
	})
	t.Run("api key request is not restricted", func(t *testing.T) {
		check(t, withAPIKeyClaim(globalUserID), globalUserID, "endpoint:read", "default", true)
		check(t, withAPIKeyClaim(globalUserID), globalUserID, "endpoint:read", "", true)
	})
	t.Run("hooks return community defaults", func(t *testing.T) {
		var binding, scope bool
		err := execWithContext(t, db, asAPIUser(globalUserID), func(tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, `
				SELECT api.check_role_binding($1::uuid, 'endpoint:read', 'default')
			`, globalUserID).Scan(&binding); err != nil {
				return err
			}
			return tx.QueryRowContext(ctx, `
				SELECT api.check_api_key_scope('endpoint:read', 'default')
			`).Scan(&scope)
		})
		if err != nil {
			t.Fatalf("calling hooks failed: %v", err)
		}
		if !binding || !scope {
			t.Fatalf("check_role_binding = %v, check_api_key_scope = %v, want true, true", binding, scope)
		}
	})
}
