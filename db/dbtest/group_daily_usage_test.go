package dbtest

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Tests for api.group_daily_usage: usage by user, Team and OrgUnit.

type groupUsageRow struct {
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	DisplayName      *string  `json:"display_name"`
	Workspace        string   `json:"workspace"`
	EndpointType     *string  `json:"endpoint_type"`
	ModelName        *string  `json:"model_name"`
	Requests         int64    `json:"requests"`
	Usage            int64    `json:"usage"`
	PromptTokens     int64    `json:"prompt_tokens"`
	CompletionTokens int64    `json:"completion_tokens"`
	CacheReadTokens  *int64   `json:"cache_read_tokens"`
	CostUSD          *float64 `json:"cost_usd"`
}

type groupUsageTotal struct{ requests, usage int64 }

// recordUsage records one request through the RPC the gateway pipeline calls.
func recordUsage(t *testing.T, keyID, model string, prompt, completion int64, cacheRead *int64) {
	t.Helper()

	if _, err := GetTestDB(t).Exec(`
		SELECT api.record_api_usage(
			p_api_key_id := $1, p_request_id := $2, p_usage_amount := $3,
			p_endpoint_name := 'gu-ep', p_endpoint_type := 'endpoint', p_model_name := $4,
			p_prompt_tokens := $5, p_completion_tokens := $6, p_cache_read_tokens := $7)`,
		keyID, newOrgName("gu-req"), prompt+completion, model, prompt, completion, cacheRead); err != nil {
		t.Fatalf("recording usage failed: %v", err)
	}
}

func aggregateUsage(t *testing.T) {
	t.Helper()

	if _, err := GetTestDB(t).Exec(`SELECT api.aggregate_usage_records(now() + interval '1 second')`); err != nil {
		t.Fatalf("aggregating failed: %v", err)
	}
}

func usageByGroup(t *testing.T, token string, params map[string]any) (int, []groupUsageRow, string) {
	t.Helper()

	body := map[string]any{"p_start_date": "1970-01-01", "p_end_date": "2999-12-31"}
	for k, v := range params {
		body[k] = v
	}

	code, raw := postgrestAs(t, token, http.MethodPost, "/rpc/get_usage_by_group", mustJSON(body))

	var rows []groupUsageRow
	if code == http.StatusOK {
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			t.Fatalf("decoding %s: %v", raw, err)
		}
	}

	return code, rows, raw
}

// groupTotals sums the rows of kind per name.
func groupTotals(t *testing.T, kind string) map[string]groupUsageTotal {
	t.Helper()

	code, rows, raw := usageByGroup(t, adminToken(t), map[string]any{"p_kind": kind})
	if code != http.StatusOK {
		t.Fatalf("get_usage_by_group(%s) = HTTP %d: %s", kind, code, raw)
	}

	out := map[string]groupUsageTotal{}

	for _, r := range rows {
		if r.Kind != kind {
			t.Fatalf("row kind %q, want %q", r.Kind, kind)
		}

		tot := out[r.Name]
		tot.requests += r.Requests
		tot.usage += r.Usage
		out[r.Name] = tot
	}

	return out
}

func TestGroupDailyUsage_Attribution(t *testing.T) {
	f := newOrgFixture(t)
	root := f.createOrgUnit("")
	leaf := f.createOrgUnit(root.Metadata.Name)
	other := f.createOrgUnit("")
	team := f.createTeam()
	team2 := f.createTeam()

	alice := createGoTrueTestUser(t, "gu-alice", "password123")
	bob := createGoTrueTestUser(t, "gu-bob", "password123")

	putInOrgUnit(t, f, leaf, alice.ID)
	addToTeam(t, f, team, alice.ID)
	putInOrgUnit(t, f, other, bob.ID)

	dropGroupUsage(t, alice, bob)
	aliceKey := createTestAPIKey(t, alice.ID, "gu-ws", 1000000)
	bobKey := createTestAPIKey(t, bob.ID, "gu-ws", 1000000)

	cache := int64(4)
	recordUsage(t, aliceKey, "m1", 10, 5, &cache)
	recordUsage(t, aliceKey, "m1", 20, 5, nil)
	recordUsage(t, aliceKey, "m2", 1, 1, nil)
	recordUsage(t, bobKey, "m1", 100, 50, nil)
	aggregateUsage(t)

	t.Run("by user, Team and OrgUnit subtree", func(t *testing.T) {
		users := groupTotals(t, "User")
		if users[alice.ID] != (groupUsageTotal{3, 42}) || users[bob.ID] != (groupUsageTotal{1, 150}) {
			t.Errorf("by user: alice %+v, bob %+v", users[alice.ID], users[bob.ID])
		}

		teams := groupTotals(t, "Team")
		if teams[team.Metadata.Name] != (groupUsageTotal{3, 42}) {
			t.Errorf("by Team: %+v, want alice only", teams[team.Metadata.Name])
		}

		units := groupTotals(t, "OrgUnit")
		for name, want := range map[string]groupUsageTotal{
			root.Metadata.Name:  {3, 42},
			leaf.Metadata.Name:  {3, 42},
			other.Metadata.Name: {1, 150},
		} {
			if units[name] != want {
				t.Errorf("by OrgUnit %s: %+v, want %+v", name, units[name], want)
			}
		}
	})

	t.Run("user total equals the key total", func(t *testing.T) {
		var keyTotal int64
		if err := execWithContext(t, GetTestDB(t), []SetContextFunc{setUserContext(alice.ID)}, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT COALESCE(SUM(usage), 0) FROM api.get_usage_by_dimension('1970-01-01', '2999-12-31', $1, NULL, NULL)`,
				aliceKey).Scan(&keyTotal)
		}); err != nil {
			t.Fatal(err)
		}

		if keyTotal != 42 {
			t.Errorf("key total = %d, want 42", keyTotal)
		}
	})

	t.Run("rows keep model, breakdown and display name", func(t *testing.T) {
		_, rows, raw := usageByGroup(t, adminToken(t), map[string]any{"p_kind": "Team", "p_name": team.Metadata.Name})

		byModel := map[string]groupUsageRow{}
		for _, r := range rows {
			byModel[*r.ModelName] = r
		}

		m1 := byModel["m1"]
		if m1.Requests != 2 || m1.PromptTokens != 30 || m1.CompletionTokens != 10 ||
			m1.CacheReadTokens == nil || *m1.CacheReadTokens != 4 || m1.EndpointType == nil || *m1.EndpointType != "endpoint" {
			t.Errorf("m1 row: %s", raw)
		}

		if m2 := byModel["m2"]; m2.Requests != 1 || m2.CacheReadTokens != nil || m2.CostUSD != nil {
			t.Errorf("m2 row (breakdown never reported stays null): %s", raw)
		}

		if len(rows) != 2 || rows[0].Workspace != "gu-ws" {
			t.Errorf("rows: %s", raw)
		}
	})

	t.Run("a later membership change does not move earlier usage", func(t *testing.T) {
		if err := f.st.DeleteTeamMember(team.ID, alice.ID); err != nil {
			t.Fatal(err)
		}

		addToTeam(t, f, team2, alice.ID)
		putInOrgUnit(t, f, other, alice.ID)

		aggregateUsage(t)

		if got := groupTotals(t, "Team")[team.Metadata.Name]; got != (groupUsageTotal{3, 42}) {
			t.Errorf("old Team after the change: %+v, want unchanged", got)
		}

		if got, ok := groupTotals(t, "Team")[team2.Metadata.Name]; ok {
			t.Errorf("new Team got earlier usage: %+v", got)
		}

		recordUsage(t, aliceKey, "m1", 7, 0, nil)
		aggregateUsage(t)

		teams := groupTotals(t, "Team")
		if teams[team.Metadata.Name] != (groupUsageTotal{3, 42}) || teams[team2.Metadata.Name] != (groupUsageTotal{1, 7}) {
			t.Errorf("after new usage: old %+v, new %+v", teams[team.Metadata.Name], teams[team2.Metadata.Name])
		}

		units := groupTotals(t, "OrgUnit")
		if units[root.Metadata.Name] != (groupUsageTotal{3, 42}) || units[other.Metadata.Name] != (groupUsageTotal{2, 157}) {
			t.Errorf("after the move: root %+v, other %+v", units[root.Metadata.Name], units[other.Metadata.Name])
		}

		if got := groupTotals(t, "User")[alice.ID]; got != (groupUsageTotal{4, 49}) {
			t.Errorf("alice: %+v", got)
		}
	})

	t.Run("an OrgUnit moved later carries its usage to the new parent", func(t *testing.T) {
		if err := f.moveOrgUnit(leaf, other.Metadata.Name); err != nil {
			t.Fatal(err)
		}

		units := groupTotals(t, "OrgUnit")
		if _, ok := units[root.Metadata.Name]; ok {
			t.Errorf("old parent still has usage: %+v", units[root.Metadata.Name])
		}

		if units[other.Metadata.Name] != (groupUsageTotal{5, 199}) {
			t.Errorf("new parent: %+v, want its own plus the moved subtree", units[other.Metadata.Name])
		}
	})

	t.Run("an Inactive Team gets no new usage", func(t *testing.T) {
		if err := f.st.UpdateTeam(team2.GetID(), &v1.Team{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
			t.Fatal(err)
		}

		recordUsage(t, aliceKey, "m1", 1, 0, nil)
		aggregateUsage(t)

		if got := groupTotals(t, "Team")[team2.Metadata.Name]; got != (groupUsageTotal{1, 7}) {
			t.Errorf("Inactive Team: %+v, want unchanged", got)
		}
	})

	t.Run("workspace filter", func(t *testing.T) {
		_, rows, raw := usageByGroup(t, adminToken(t), map[string]any{"p_kind": "User", "p_name": bob.ID, "p_workspace": "gu-other-ws"})
		if len(rows) != 0 {
			t.Errorf("other workspace: %s", raw)
		}
	})
}

func TestGroupDailyUsage_Permission(t *testing.T) {
	user := createGoTrueTestUser(t, "gu-self", "password123")
	self := gotruePasswordLogin(t, user.Email, "password123").AccessToken

	for name, token := range map[string]string{
		"a user without a role":             self,
		"team:read and group_policy:read":   userTokenWithPermissions(t, "gu-reader", []string{"team:read", "group_policy:read"}),
		"workspace:read without usage-read": userTokenWithPermissions(t, "gu-ws-read", []string{"workspace:read"}),
	} {
		if code, _, raw := usageByGroup(t, token, map[string]any{"p_kind": "User", "p_name": user.ID}); code != http.StatusForbidden {
			t.Errorf("%s: HTTP %d: %s; want 403", name, code, raw)
		}
	}

	reader := userTokenWithPermissions(t, "gu-usage", []string{"workspace:read", "workspace:usage-read"})
	if code, _, raw := usageByGroup(t, reader, map[string]any{"p_kind": "Team"}); code != http.StatusOK {
		t.Errorf("global workspace:usage-read: HTTP %d: %s", code, raw)
	}

	if code, _, raw := usageByGroup(t, adminToken(t), map[string]any{"p_kind": "Workspace"}); code != http.StatusBadRequest {
		t.Errorf("unknown kind: HTTP %d: %s; want 400", code, raw)
	}

	code, raw := postgrestAs(t, adminToken(t), http.MethodGet, "/group_daily_usage", "")
	if code == http.StatusOK && raw != "[]" {
		t.Errorf("direct read: HTTP %d: %s; want nothing", code, raw)
	}

	code, raw = postgrestAs(t, adminToken(t), http.MethodPost, "/rpc/group_daily_usage_on_aggregated", "{}")
	if code == http.StatusOK {
		t.Errorf("trigger function as RPC: HTTP %d: %s", code, raw)
	}

}

// org_unit:usage-read reads users and OrgUnits, never Teams. A global
// assignment of it covers every user with an OrgUnit.
func TestGroupDailyUsage_OrgUnitUsageRead(t *testing.T) {
	f := newOrgFixture(t)
	unit := f.createOrgUnit("")
	team := f.createTeam()

	member := createGoTrueTestUser(t, "gu-oumember", "password123")
	loner := createGoTrueTestUser(t, "gu-ouloner", "password123")

	putInOrgUnit(t, f, unit, member.ID)
	addToTeam(t, f, team, member.ID)

	dropGroupUsage(t, member, loner)
	recordUsage(t, createTestAPIKey(t, member.ID, "gu-ws", 1000000), "m1", 10, 5, nil)
	recordUsage(t, createTestAPIKey(t, loner.ID, "gu-ws", 1000000), "m1", 20, 5, nil)
	aggregateUsage(t)

	for name, token := range map[string]string{
		"org_unit:usage-read":       userTokenWithPermissions(t, "gu-ouread", []string{"org_unit:usage-read"}),
		"department-admin (global)": tokenWithPresetRole(t, "gu-oudept", "department-admin"),
	} {
		t.Run(name, func(t *testing.T) {
			code, rows, raw := usageByGroup(t, token, map[string]any{"p_kind": "User"})
			if code != http.StatusOK {
				t.Fatalf("by user = HTTP %d: %s", code, raw)
			}

			users := map[string]int64{}
			for _, r := range rows {
				users[r.Name] += r.Usage
			}

			if users[member.ID] != 15 {
				t.Errorf("member: %d, want 15", users[member.ID])
			}

			if _, ok := users[loner.ID]; ok {
				t.Errorf("a user in no OrgUnit is visible: %s", raw)
			}

			code, rows, raw = usageByGroup(t, token, map[string]any{"p_kind": "OrgUnit", "p_name": unit.Metadata.Name})
			if code != http.StatusOK || len(rows) != 1 || rows[0].Usage != 15 {
				t.Errorf("by OrgUnit = HTTP %d: %s", code, raw)
			}

			if code, _, raw := usageByGroup(t, token, map[string]any{"p_kind": "Team"}); code != http.StatusForbidden {
				t.Errorf("by Team = HTTP %d: %s; want 403", code, raw)
			}
		})
	}
}
