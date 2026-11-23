package dbtest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Tests for group token pools: one token pool shared by a group.

const poolWorkspace = "pool-ws"

// dropGroupUsage removes the group usage of users when the test ends; not
// per key, as a subtest's key would take the whole test's usage with it.
func dropGroupUsage(t *testing.T, users ...*TestUser) {
	t.Cleanup(func() {
		for _, u := range users {
			_, _ = GetTestDB(t).Exec(`DELETE FROM api.group_daily_usage WHERE user_id = $1`, u.ID)
		}
	})
}

// setPool stores a policy with only a token pool (no RLS) and returns its
// name; it is removed when the test ends.
func setPool(t *testing.T, kind, subject string, limit int64, period string) string {
	t.Helper()

	db := GetTestDB(t)
	name := newOrgName("pool")

	if _, err := db.Exec(`
		INSERT INTO api.group_policies (api_version, kind, metadata, spec)
		VALUES ('v1', 'GroupPolicy',
			ROW($1, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
			ROW($2, $3, NULL, NULL, jsonb_build_object('limit', $4::bigint, 'period', $5::text), NULL, NULL)::api.group_policy_spec)`,
		name, kind, subject, limit, period); err != nil {
		t.Fatalf("setting the pool of %s %s failed: %v", kind, subject, err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.group_policies WHERE (metadata).name = $1`, name)
	})

	return name
}

func setServiceRoleContext() SetContextFunc {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(`SET LOCAL request.jwt.claims = '{"role": "service_role"}'`)
		return err
	}
}

// remaining calls get_api_key_remaining as the gateway does; -1 << 62 stands
// for NULL (no limit).
const noLimit = int64(-1 << 62)

func remaining(t *testing.T, keyID string, model any) int64 {
	t.Helper()

	var rem sql.NullInt64
	if err := execWithContext(t, GetTestDB(t), []SetContextFunc{setServiceRoleContext()}, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT api.get_api_key_remaining($1, $2)`, keyID, model).Scan(&rem)
	}); err != nil {
		t.Fatalf("get_api_key_remaining failed: %v", err)
	}

	if !rem.Valid {
		return noLimit
	}

	return rem.Int64
}

func TestGroupTokenPool_Remaining(t *testing.T) {
	f := newOrgFixture(t)
	root := f.createOrgUnit("")
	leaf := f.createOrgUnit(root.Metadata.Name)
	team := f.createTeam()

	alice := createGoTrueTestUser(t, "pool-alice", "password123")
	bob := createGoTrueTestUser(t, "pool-bob", "password123")
	carol := createGoTrueTestUser(t, "pool-carol", "password123")
	dave := createGoTrueTestUser(t, "pool-dave", "password123")
	dropGroupUsage(t, alice, bob, carol, dave)

	addToTeam(t, f, team, alice.ID)
	addToTeam(t, f, team, bob.ID)
	putInOrgUnit(t, f, leaf, dave.ID)

	aliceKey := createTestAPIKey(t, alice.ID, poolWorkspace, 0)
	bobKey := createTestAPIKey(t, bob.ID, poolWorkspace, 0)
	carolKey := createTestAPIKey(t, carol.ID, poolWorkspace, 0)
	daveKey := createTestAPIKey(t, dave.ID, poolWorkspace, 0)

	if got := remaining(t, aliceKey, nil); got != noLimit {
		t.Fatalf("no pool yet: remaining %d, want no limit", got)
	}

	setPool(t, "Team", team.Metadata.Name, 100, "monthly")

	t.Run("members draw from one pool; a non-member is not limited", func(t *testing.T) {
		recordUsage(t, aliceKey, "m1", 20, 10, nil)
		recordUsage(t, bobKey, "m2", 40, 10, nil)
		recordUsage(t, carolKey, "m1", 500, 0, nil)
		aggregateUsage(t)

		if a, b := remaining(t, aliceKey, nil), remaining(t, bobKey, "m1"); a != 20 || b != 20 {
			t.Errorf("remaining: alice %d, bob %d; want 20 each", a, b)
		}

		if got := remaining(t, carolKey, nil); got != noLimit {
			t.Errorf("non-member: %d, want no limit", got)
		}
	})

	t.Run("a key quota smaller than the pool wins", func(t *testing.T) {
		small := createTestAPIKey(t, alice.ID, poolWorkspace, 5)
		if got := remaining(t, small, nil); got != 5 {
			t.Errorf("key 5 vs pool 20: %d", got)
		}

		big := createTestAPIKey(t, alice.ID, poolWorkspace, 1000)
		if got := remaining(t, big, nil); got != 20 {
			t.Errorf("key 1000 vs pool 20: %d", got)
		}
	})

	t.Run("a per-model key without a model on the call still gets the pool", func(t *testing.T) {
		var keyID string
		if err := execWithContext(t, GetTestDB(t), []SetContextFunc{setUserContext(alice.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT id FROM api.create_api_key(p_workspace := $1, p_name := $2, p_quota := 0,
				p_limits := '{"allowed_models": [{"model": "m1", "token_limit": 3}]}'::jsonb)`,
				poolWorkspace, newOrgName("pool-pm")).Scan(&keyID)
		}); err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _, _ = GetTestDB(t).Exec(`DELETE FROM api.api_keys WHERE id = $1`, keyID) })

		if got := remaining(t, keyID, nil); got != 20 {
			t.Errorf("no model: %d, want the pool's 20", got)
		}

		if got := remaining(t, keyID, "m1"); got != 3 {
			t.Errorf("m1: %d, want the entry's 3", got)
		}
	})

	t.Run("an exhausted pool stops every member", func(t *testing.T) {
		recordUsage(t, aliceKey, "m1", 30, 0, nil)
		aggregateUsage(t)

		if a, b := remaining(t, aliceKey, nil), remaining(t, bobKey, nil); a != -10 || b != -10 {
			t.Errorf("remaining: alice %d, bob %d; want -10 each", a, b)
		}
	})

	t.Run("an OrgUnit pool covers its subtree", func(t *testing.T) {
		setPool(t, "OrgUnit", root.Metadata.Name, 1000, "monthly")

		recordUsage(t, daveKey, "m1", 100, 0, nil)
		aggregateUsage(t)

		if got := remaining(t, daveKey, nil); got != 900 {
			t.Errorf("leaf member: %d, want 900", got)
		}

		if got := remaining(t, carolKey, nil); got != noLimit {
			t.Errorf("outside the subtree: %d, want no limit", got)
		}
	})

	t.Run("every pool must have room", func(t *testing.T) {
		putInOrgUnit(t, f, leaf, alice.ID)

		if got := remaining(t, aliceKey, nil); got != -10 {
			t.Errorf("Team -10 and OrgUnit 900: %d", got)
		}

		addToTeam(t, f, team, dave.ID)

		if got := remaining(t, daveKey, nil); got != -10 {
			t.Errorf("dave joined the exhausted Team: %d, want -10", got)
		}
	})

	t.Run("leaving a group leaves its usage behind", func(t *testing.T) {
		if err := f.st.DeleteTeamMember(team.ID, bob.ID); err != nil {
			t.Fatal(err)
		}

		if got := remaining(t, bobKey, nil); got != noLimit {
			t.Errorf("bob after leaving: %d, want no limit", got)
		}

		if got := remaining(t, aliceKey, nil); got != -10 {
			t.Errorf("Team pool after bob left: %d, want -10 (his usage stays)", got)
		}
	})

	t.Run("an Inactive Team still limits", func(t *testing.T) {
		if err := f.st.UpdateTeam(team.GetID(), &v1.Team{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
			t.Fatal(err)
		}

		if got := remaining(t, aliceKey, nil); got != -10 {
			t.Errorf("Inactive Team: %d, want -10", got)
		}
	})

	t.Run("the period window", func(t *testing.T) {
		u := createGoTrueTestUser(t, "pool-period", "password123")
		dropGroupUsage(t, u)
		teamP := f.createTeam()
		addToTeam(t, f, teamP, u.ID)
		k := createTestAPIKey(t, u.ID, poolWorkspace, 0)

		if _, err := GetTestDB(t).Exec(`
			INSERT INTO api.group_daily_usage (usage_date, user_id, team_ids, workspace, endpoint_type, endpoint_name, model_name, requests, total_tokens)
			VALUES (CURRENT_DATE - 1, $1, ARRAY[$2::int], $3, 'endpoint', 'gu-ep', 'm1', 1, 70)`,
			u.ID, teamP.ID, poolWorkspace); err != nil {
			t.Fatal(err)
		}

		name := setPool(t, "Team", teamP.Metadata.Name, 100, "daily")
		if got := remaining(t, k, nil); got != 100 {
			t.Errorf("daily pool with yesterday's usage: %d, want 100", got)
		}

		if _, err := GetTestDB(t).Exec(`UPDATE api.group_policies SET spec.token_quota = '{"limit": 100, "period": "yearly"}'
			WHERE (metadata).name = $1`, name); err != nil {
			t.Fatal(err)
		}

		// CURRENT_DATE - 1 is last year on January 1st.
		var newYear bool
		_ = GetTestDB(t).QueryRow(`SELECT extract(doy FROM CURRENT_DATE) = 1`).Scan(&newYear)

		if got := remaining(t, k, nil); !newYear && got != 30 {
			t.Errorf("yearly pool: %d, want 30", got)
		}
	})

	t.Run("the owner sees the pool", func(t *testing.T) {
		owner := func(uid string) int64 {
			var rem sql.NullInt64
			if err := execWithContext(t, GetTestDB(t), []SetContextFunc{setUserContext(uid), setJwtSecretContext()}, func(tx *sql.Tx) error {
				return tx.QueryRow(`SELECT api.get_api_key_remaining($1)`, aliceKey).Scan(&rem)
			}); err != nil {
				t.Fatalf("get_api_key_remaining: %v", err)
			}

			if !rem.Valid {
				return noLimit
			}

			return rem.Int64
		}

		if got := owner(alice.ID); got != -10 {
			t.Errorf("owner: %d, want -10", got)
		}
	})
}

func TestGroupTokenPool_ModelRulesFlag(t *testing.T) {
	f := newOrgFixture(t)
	team := f.createTeam()
	member := createGoTrueTestUser(t, "pool-flag", "password123")
	other := createGoTrueTestUser(t, "pool-noflag", "password123")
	addToTeam(t, f, team, member.ID)

	flag := func(userID string) bool {
		var out string
		if err := GetTestDB(t).QueryRow(`SELECT api.get_api_key_model_rules($1::uuid, $2, NULL)::text`,
			userID, poolWorkspace).Scan(&out); err != nil {
			t.Fatal(err)
		}

		var rules struct {
			Pool *bool `json:"group_token_pool"`
		}
		if err := json.Unmarshal([]byte(out), &rules); err != nil || rules.Pool == nil {
			t.Fatalf("group_token_pool missing in %s: %v", out, err)
		}

		return *rules.Pool
	}

	if flag(member.ID) {
		t.Error("no pool yet: flag set")
	}

	// A policy with only model rules is not a pool.
	setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("a"), nil)

	if flag(member.ID) {
		t.Error("model rules only: flag set")
	}

	_, _ = GetTestDB(t).Exec(`UPDATE api.group_policies SET spec.token_quota = '{"limit": 10}'
		WHERE (spec).subject_kind = 'Team' AND (spec).subject_name = $1`, team.Metadata.Name)

	if !flag(member.ID) || flag(other.ID) {
		t.Errorf("with a pool: member %v, other %v; want true, false", flag(member.ID), flag(other.ID))
	}
}

func poolPolicyBody(team string, quota any) string {
	return mustJSON(map[string]any{
		"api_version": "v1",
		"kind":        "GroupPolicy",
		"metadata":    map[string]any{},
		"spec": map[string]any{
			"subject_kind": "Team",
			"subject_name": team,
			"token_quota":  quota,
		},
	})
}

func TestGroupTokenPool_Write(t *testing.T) {
	admin := adminToken(t)
	team := newOrgName("pool-t")
	createManualTeam(t, admin, team)

	for name, quota := range map[string]any{
		"negative limit":   map[string]any{"limit": -1},
		"fractional limit": map[string]any{"limit": 1.5},
		"string limit":     map[string]any{"limit": "10"},
		"unknown period":   map[string]any{"limit": 10, "period": "hourly"},
		"not an object":    []any{10},
	} {
		code, raw := postgrestAs(t, admin, http.MethodPost, "/group_policies", poolPolicyBody(team, quota))
		if code != http.StatusBadRequest || !strings.Contains(raw, "10303") {
			t.Errorf("%s: HTTP %d: %s; want 400 10303", name, code, raw)
		}
	}

	code, raw := postgrestAs(t, admin, http.MethodPost, "/group_policies", poolPolicyBody(team, map[string]any{"limit": 500}))
	if code != http.StatusCreated {
		t.Fatalf("POST = HTTP %d: %s", code, raw)
	}

	var created []v1.GroupPolicy
	if err := json.Unmarshal([]byte(raw), &created); err != nil || len(created) != 1 {
		t.Fatalf("POST returned %s: %v", raw, err)
	}

	id := created[0].ID

	t.Cleanup(func() {
		_, _ = GetTestDB(t).Exec(`DELETE FROM api.group_policies WHERE id = $1`, id)
	})

	if q := created[0].Spec.TokenQuota; q == nil || q.Limit != 500 || q.Period != "monthly" {
		t.Errorf("stored token_quota: %+v, want 500 monthly", q)
	}

	path := fmt.Sprintf("/group_policies?id=eq.%d", id)

	code, raw = postgrestAs(t, admin, http.MethodPatch, path, mustJSON(map[string]any{
		"spec": map[string]any{"subject_kind": "Team", "subject_name": team, "token_quota": map[string]any{"limit": 7, "period": "weekly"}},
	}))
	if code/100 != 2 {
		t.Fatalf("PATCH = HTTP %d: %s", code, raw)
	}

	var stored string
	if err := GetTestDB(t).QueryRow(`SELECT (spec).token_quota::text FROM api.group_policies WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}

	if stored != `{"limit": 7, "period": "weekly"}` {
		t.Errorf("after PATCH: %s", stored)
	}

	code, raw = postgrestAs(t, admin, http.MethodPatch, path, mustJSON(map[string]any{
		"spec": map[string]any{"subject_kind": "Team", "subject_name": team, "token_quota": map[string]any{"limit": nil}},
	}))
	if code/100 != 2 {
		t.Fatalf("PATCH limit null = HTTP %d: %s", code, raw)
	}

	var cleared sql.NullString
	_ = GetTestDB(t).QueryRow(`SELECT (spec).token_quota::text FROM api.group_policies WHERE id = $1`, id).Scan(&cleared)

	if cleared.Valid {
		t.Errorf("limit null should clear the pool, got %s", cleared.String)
	}
}

func TestGroupTokenPool_Reads(t *testing.T) {
	f := newOrgFixture(t)
	team := f.createTeam()
	member := createGoTrueTestUser(t, "pool-read", "password123")
	dropGroupUsage(t, member)
	addToTeam(t, f, team, member.ID)
	k := createTestAPIKey(t, member.ID, poolWorkspace, 0)

	setPool(t, "Team", team.Metadata.Name, 1000, "monthly")
	recordUsage(t, k, "m1", 100, 23, nil)

	if _, err := GetTestDB(t).Exec(`UPDATE api.teams SET metadata.display_name = 'Pool Team' WHERE id = $1`, team.ID); err != nil {
		t.Fatal(err)
	}
	aggregateUsage(t)

	type pool struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		TokenLimit int64  `json:"token_limit"`
		Period     string `json:"period"`
		Used       int64  `json:"used"`
		Remaining  int64  `json:"remaining"`
		ResetsAt   string `json:"resets_at"`
	}

	t.Run("get_group_token_pools", func(t *testing.T) {
		code, raw := postgrestAs(t, adminToken(t), http.MethodPost, "/rpc/get_group_token_pools",
			mustJSON(map[string]any{"p_kind": "Team", "p_name": team.Metadata.Name}))
		if code != http.StatusOK {
			t.Fatalf("admin: HTTP %d: %s", code, raw)
		}

		var rows []pool
		if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("rows %s: %v", raw, err)
		}

		if r := rows[0]; r.TokenLimit != 1000 || r.Used != 123 || r.Remaining != 877 || r.Period != "monthly" || r.ResetsAt == "" {
			t.Errorf("pool: %+v", r)
		}

		self := gotruePasswordLogin(t, member.Email, "password123").AccessToken
		if code, raw := postgrestAs(t, self, http.MethodPost, "/rpc/get_group_token_pools", "{}"); code != http.StatusForbidden {
			t.Errorf("member: HTTP %d: %s; want 403", code, raw)
		}
	})

	t.Run("get_user_group_policy lists the member's pools", func(t *testing.T) {
		self := gotruePasswordLogin(t, member.Email, "password123").AccessToken

		code, raw := postgrestAs(t, self, http.MethodPost, "/rpc/get_user_group_policy",
			mustJSON(map[string]any{"p_user_id": member.ID, "p_workspace": poolWorkspace}))
		if code != http.StatusOK {
			t.Fatalf("HTTP %d: %s", code, raw)
		}

		var out struct {
			Groups []struct {
				TokenQuota *v1.GroupTokenQuota `json:"token_quota"`
			} `json:"groups"`
			Pools []struct {
				pool
				DisplayName string `json:"display_name"`
			} `json:"token_pools"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}

		if len(out.Pools) != 1 || out.Pools[0].Used != 123 || out.Pools[0].Remaining != 877 || out.Pools[0].Name != team.Metadata.Name ||
			out.Pools[0].DisplayName != "Pool Team" {
			t.Errorf("token_pools: %s", raw)
		}

		if len(out.Groups) != 1 || out.Groups[0].TokenQuota == nil || out.Groups[0].TokenQuota.Limit != 1000 {
			t.Errorf("groups: %s", raw)
		}
	})
}
