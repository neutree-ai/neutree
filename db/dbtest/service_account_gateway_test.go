package dbtest

import (
	"database/sql"
	"testing"
)

// Tests for what the gateway enforces on a ServiceAccount key: its Team's
// application permissions (model:invoke), group policy and token pool, and
// usage counted for the SA and its Team.

func TestServiceAccountGateway_Access(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)

	if !hasPermission(t, sa, "model:invoke", saWorkspace) {
		t.Fatal("an SA under the default application permissions may not call models")
	}

	t.Run("no allow list is no limit", func(t *testing.T) {
		if allowed, denied := keyModelRules(t, sa, saWorkspace, nil); allowed != "null" || len(denied) != 0 {
			t.Errorf("no group policy: allowed %s, denied %v", allowed, denied)
		}
	})

	t.Run("the Team's group policy narrows the key, another Team's does not reach it", func(t *testing.T) {
		setGroupPolicy(t, "Team", f.team, gpModels("m1", "m2"), gpModels("m2"))
		setGroupPolicy(t, "Team", f.other, gpModels("m3"), nil)
		mustExec(t, f.db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.otherID, f.member)

		if allowed, denied := keyModelRules(t, sa, saWorkspace, nil); allowed != wantModels("m1") || len(denied) != 1 || denied[0] != "m2" {
			t.Errorf("no key list: allowed %s, denied %v; want m1, [m2]", allowed, denied)
		}

		if allowed, _ := keyModelRules(t, sa, saWorkspace, gpModels("m1", "m3")); allowed != wantModels("m1") {
			t.Errorf("key list m1, m3: allowed %s, want m1", allowed)
		}
	})
}

func TestServiceAccountGateway_InvokableModels(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)

	insertServedModelsEndpoint(t, f.db, newOrgName("sa-ep"), saWorkspace, servedEndpointStatus("sa-model"), false)
	insertServedModelsEndpoint(t, f.db, newOrgName("sa-ep"), saWorkspace, servedEndpointStatus("sa-outside"), false)

	list := func(user string) ([]string, error) {
		var models []string

		err := asUser(t, f.db, user, nil, func(tx *sql.Tx) error {
			rows, err := tx.Query(`SELECT model FROM api.list_invokable_models($1, $2::uuid) WHERE model LIKE 'sa-%'`, saWorkspace, sa)
			if err != nil {
				return err
			}
			defer rows.Close()

			for rows.Next() {
				var m string
				if err := rows.Scan(&m); err != nil {
					return err
				}

				models = append(models, m)
			}

			return rows.Err()
		})

		return models, err
	}

	setAppPermissions(t, f.db, f.teamID, []string{customRole(t, f.db, "endpoint:read")}, nil)

	if models, err := list(f.admin); err != nil || len(models) != 0 {
		t.Fatalf("SA without model:invoke: %v, %v; want none", models, err)
	}

	mustExec(t, f.db, `DELETE FROM api.team_application_permissions WHERE team_id = $1`, f.teamID)

	if models, err := list(f.admin); err != nil || len(models) != 2 {
		t.Fatalf("SA with model:invoke, no group policy: %v, %v; want both", models, err)
	}

	setGroupPolicy(t, "Team", f.team, gpModels("sa-model"), nil)

	if models, err := list(f.admin); err != nil || len(models) != 1 || models[0] != "sa-model" {
		t.Fatalf("SA under a group policy: %v, %v; want sa-model", models, err)
	}

	_, err := list(f.nobody)
	wantErr(t, "a user who cannot read the SA", err, "service_account:read on the Team of the ServiceAccount is required")
}

func TestServiceAccountGateway_UsageAndPool(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	setPool(t, "Team", f.team, 100, "monthly")

	saKey := f.mustSAKey(t, sa, nil)
	memberKey := createTestAPIKey(t, f.member, saWorkspace, 0)

	recordUsage(t, saKey, "m1", 30, 10, nil)
	recordUsage(t, memberKey, "m1", 15, 5, nil)
	aggregateUsage(t)

	t.Run("an SA key's usage is recorded for the SA and its Team", func(t *testing.T) {
		var n int
		if err := f.db.QueryRow(`
			SELECT count(*) FROM api.group_daily_usage
			WHERE user_id IS NULL AND service_account_id = $1 AND org_unit_id IS NULL
			  AND team_ids = ARRAY[$2]::integer[] AND total_tokens = 40`, sa, f.teamID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("SA key rows: %d, %v", n, err)
		}
	})

	t.Run("the SA key and the members draw from one pool", func(t *testing.T) {
		if got := remaining(t, saKey, nil); got != 40 {
			t.Errorf("SA key: remaining %d, want 100 - 40 - 20 = 40", got)
		}

		if got := remaining(t, memberKey, nil); got != 40 {
			t.Errorf("member key: remaining %d, want 40", got)
		}
	})

	total := func(kind string, name any) (int64, error) {
		var tokens int64

		err := asUser(t, f.db, f.admin, nil, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COALESCE(sum(usage), 0) FROM api.get_usage_by_group('1970-01-01', '2999-12-31', $1, $2, NULL, NULL, $3)`,
				kind, name, f.team).Scan(&tokens)
		})

		return tokens, err
	}

	t.Run("team-admin reads usage by SA and by Team", func(t *testing.T) {
		if got, err := total("ServiceAccount", nil); err != nil || got != 40 {
			t.Errorf("by ServiceAccount: %d, %v; want 40", got, err)
		}

		if got, err := total("ServiceAccount", sa); err != nil || got != 40 {
			t.Errorf("one ServiceAccount: %d, %v; want 40", got, err)
		}

		if got, err := total("Team", f.team); err != nil || got != 60 {
			t.Errorf("by Team: %d, %v; want 60", got, err)
		}

		if got, err := total("User", nil); err != nil || got != 20 {
			t.Errorf("by user: %d, %v; want the member's 20 only", got, err)
		}

		err := asUser(t, f.db, f.nobody, nil, func(tx *sql.Tx) error {
			_, err := tx.Exec(`SELECT * FROM api.get_usage_by_group('1970-01-01', '2999-12-31', 'ServiceAccount', NULL, NULL, NULL, $1)`, f.team)
			return err
		})
		wantErr(t, "another user reading SA usage", err, "usage-read is required")
	})

	t.Run("team-admin sees the SA key's usage and quota", func(t *testing.T) {
		quota := f.mustSAKey(t, sa, map[string]any{"token_quota": map[string]any{"limit": 1000}})

		if n := countAs(t, f.db, f.admin, nil, `SELECT count(*) FROM api.get_api_keys_usage_summary($1, ARRAY[$2]::uuid[])`, saWorkspace, quota); n != 1 {
			t.Fatalf("team-admin: %d summary rows, want 1", n)
		}

		if n := countAs(t, f.db, f.admin, nil, `SELECT count(*) FROM api.get_usage_by_dimension('1970-01-01', '2999-12-31', $1)`, saKey); n == 0 {
			t.Fatal("team-admin sees no usage of the SA key by dimension")
		}

		if n := countAs(t, f.db, f.nobody, nil, `SELECT count(*) FROM api.get_api_keys_usage_summary($1, ARRAY[$2]::uuid[])`, saWorkspace, quota); n != 0 {
			t.Fatalf("another user: %d summary rows, want 0", n)
		}
	})
}

// An SA reaches the control plane only through its Team's application
// permissions.
func TestServiceAccountGateway_ControlPlaneRead(t *testing.T) {
	f := newSAFixture(t)
	sa := f.mustSA(t)
	reader := customRole(t, f.db, "endpoint:read")
	ep := newOrgName("sa-cp-ep")
	insertServedModelsEndpoint(t, f.db, ep, saWorkspace, servedEndpointStatus("sa-cp-model"), false)

	read := func() int {
		return countAs(t, f.db, sa, nil, `SELECT count(*) FROM api.endpoints WHERE (metadata).name = $1`, ep)
	}

	if n := read(); n != 0 {
		t.Fatalf("an SA with model-invoker only reads %d endpoints", n)
	}

	setAppPermissions(t, f.db, f.teamID, []string{"model-invoker", reader}, nil)

	if n := read(); n != 1 {
		t.Fatalf("an SA with endpoint:read reads %d endpoints, want 1", n)
	}

	if n := countAs(t, f.db, sa, nil, `SELECT count(*) FROM api.service_accounts`); n != 0 {
		t.Fatalf("the SA reads %d ServiceAccounts", n)
	}
}
