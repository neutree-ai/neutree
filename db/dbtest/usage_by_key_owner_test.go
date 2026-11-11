package dbtest

import (
	"database/sql"
	"testing"
)

// p_user_id / p_service_account_id narrow a Team's usage by key to one
// member's or one application's keys; they never widen what the caller reads.
func TestUsageByDimension_OwnerFilterInTeam(t *testing.T) {
	f := newSAFixture(t)
	ws := saWorkspace

	sa := f.mustSA(t)
	saKey := f.mustSAKey(t, sa, nil)

	peer := rawUser(t, f.db, "own-peer")
	mustExec(t, f.db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.teamID, peer)

	k1 := createTestAPIKey(t, f.member, ws, 0)
	k2 := createTestAPIKey(t, f.member, ws, 0)
	peerKey := createTestAPIKey(t, peer, ws, 0)

	recordUsage(t, k1, "m1", 10, 0, nil)
	recordUsage(t, k2, "m1", 20, 0, nil)
	recordUsage(t, peerKey, "m1", 40, 0, nil)
	recordUsage(t, saKey, "m1", 80, 0, nil)
	aggregateUsage(t)

	in := func(owner dimFilter) dimFilter {
		owner.workspace, owner.team = ws, f.team
		return owner
	}

	t.Run("no owner: every key of the Team, as before", func(t *testing.T) {
		got := dimensionUsage(t, f.db, f.admin, nil, in(dimFilter{}))
		if len(got) != 4 || got[k1] != 10 || got[k2] != 20 || got[peerKey] != 40 || got[saKey] != 80 {
			t.Errorf("got %v", got)
		}
	})

	t.Run("a member: only that member's keys", func(t *testing.T) {
		got := dimensionUsage(t, f.db, f.admin, nil, in(dimFilter{user: f.member}))
		if len(got) != 2 || got[k1] != 10 || got[k2] != 20 {
			t.Errorf("got %v", got)
		}
	})

	t.Run("an application: only its keys", func(t *testing.T) {
		got := dimensionUsage(t, f.db, f.admin, nil, in(dimFilter{sa: sa}))
		if len(got) != 1 || got[saKey] != 80 {
			t.Errorf("got %v", got)
		}
	})

	t.Run("a reader without the Team reads nothing", func(t *testing.T) {
		for _, p := range []dimFilter{in(dimFilter{user: f.member}), in(dimFilter{sa: sa})} {
			if got := dimensionUsage(t, f.db, f.nobody, nil, p); len(got) != 0 {
				t.Errorf("%v: got %v", p, got)
			}
		}

		if got := dimensionUsage(t, f.db, peer, nil, in(dimFilter{user: f.member})); len(got) != 0 {
			t.Errorf("another member: got %v", got)
		}
	})

	t.Run("an owner filter without a scope reads only what the caller reads", func(t *testing.T) {
		if got := dimensionUsage(t, f.db, f.nobody, nil, dimFilter{workspace: ws, user: f.member}); len(got) != 0 {
			t.Errorf("another user: got %v", got)
		}

		if got := dimensionUsage(t, f.db, f.member, nil, dimFilter{workspace: ws, user: f.member}); len(got) != 2 || got[k1] != 10 {
			t.Errorf("the owner: got %v", got)
		}
	})

	t.Run("usage outside the Team stays out", func(t *testing.T) {
		// The member's usage in another Team: not this Team's.
		mustExec(t, f.db, `INSERT INTO api.team_members (team_id, user_id) VALUES ($1, $2)`, f.otherID, f.member)
		mustExec(t, f.db, `DELETE FROM api.team_members WHERE team_id = $1 AND user_id = $2`, f.teamID, f.member)
		recordUsage(t, k1, "m1", 1000, 0, nil)
		aggregateUsage(t)

		got := dimensionUsage(t, f.db, f.admin, nil, in(dimFilter{user: f.member}))
		if len(got) != 2 || got[k1] != 10 || got[k2] != 20 {
			t.Errorf("got %v", got)
		}
	})
}

// p_direct reads a department's own usage by key, as a user's row in its
// breakdown counts it; without it, the subtree.
func TestUsageByDimension_OwnerFilterInOrgUnit(t *testing.T) {
	db := GetTestDB(t)
	o := setupOrgTree(t, db)
	ws := "ud-owner-ws-" + o.suffix

	ua := createTestAPIKey(t, o.users["ua"], ws, 0)
	ua1 := createTestAPIKey(t, o.users["ua1"], ws, 0)
	ub := createTestAPIKey(t, o.users["ub"], ws, 0)

	// ua's 5 tokens were recorded while ua was in a1, the 7 while in b.
	usage := func(tx *sql.Tx) {
		for _, r := range []struct {
			user, unit, key string
			tokens          int
		}{{"ua", "a", ua, 10}, {"ua", "a1", ua, 5}, {"ua", "b", ua, 7}, {"ua1", "a1", ua1, 20}, {"ub", "b", ub, 80}} {
			mustTxExec(t, tx, `
				INSERT INTO api.group_daily_usage (usage_date, user_id, org_unit_id, workspace,
				    endpoint_type, endpoint_name, model_name, requests, total_tokens, api_key_id)
				VALUES (CURRENT_DATE, $1, $2, $3, 'endpoint', 'ep', 'm', 1, $4, $5)`,
				o.users[r.user], o.units[r.unit], ws, r.tokens, r.key)
		}
	}

	dadmin := o.users["dadmin"]
	in := func(direct bool) dimFilter {
		return dimFilter{workspace: ws, orgUnit: o.unit("a"), user: o.users["ua"], direct: direct}
	}

	if got := dimensionUsage(t, db, dadmin, usage, in(true)); len(got) != 1 || got[ua] != 10 {
		t.Errorf("direct: got %v, want ua's key with 10", got)
	}

	if got := dimensionUsage(t, db, dadmin, usage, in(false)); len(got) != 1 || got[ua] != 15 {
		t.Errorf("subtree: got %v, want ua's key with 15", got)
	}

	if got := dimensionUsage(t, db, dadmin, usage, dimFilter{workspace: ws, orgUnit: o.unit("b"), user: o.users["ub"]}); len(got) != 0 {
		t.Errorf("outside the subtree: got %v", got)
	}

	if got := dimensionUsage(t, db, o.users["nobody"], usage, in(true)); len(got) != 0 {
		t.Errorf("no department access: got %v", got)
	}

	// The same total as the user's row in the department's breakdown.
	var byGroup int64
	if err := asUser(t, db, dadmin, usage, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COALESCE(SUM(usage), 0) FROM api.get_usage_by_group(
		    p_start_date => '1970-01-01', p_end_date => '2999-12-31', p_kind => 'User',
		    p_name => $1, p_workspace => $2, p_org_unit => $3, p_direct => TRUE)`,
			o.users["ua"], ws, o.unit("a")).Scan(&byGroup)
	}); err != nil || byGroup != 10 {
		t.Errorf("usage by group = %d, %v; want 10", byGroup, err)
	}
}
