package dbtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Tests for group policies: model access by OrgUnit or Team.

const gpWorkspace = "gp-ws"

func gpModels(names ...string) []map[string]any {
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		model, ws, _ := strings.Cut(n, "@")

		entry := map[string]any{"model": model}
		if ws != "" {
			entry["workspace"] = ws
		}

		out = append(out, entry)
	}

	return out
}

// setGroupPolicy stores a policy directly (no RLS) and removes it when the
// test ends. A nil list is no rule.
func setGroupPolicy(t *testing.T, kind, subject string, allowed, denied []map[string]any) {
	t.Helper()

	db := GetTestDB(t)
	name := newOrgName("gp")

	toJSON := func(v []map[string]any) any {
		if v == nil {
			return nil
		}

		return mustJSON(v)
	}

	if _, err := db.Exec(`
		INSERT INTO api.group_policies (api_version, kind, metadata, spec)
		VALUES ('v1', 'GroupPolicy',
			ROW($1, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
			ROW($2, $3, $4::jsonb, $5::jsonb, NULL, NULL, NULL)::api.group_policy_spec)`,
		name, kind, subject, toJSON(allowed), toJSON(denied)); err != nil {
		t.Fatalf("setting the policy of %s %s failed: %v", kind, subject, err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.group_policies WHERE (metadata).name = $1`, name)
	})
}

// keyModelRules calls api.get_api_key_model_rules as the controller does and
// returns its allowed_models as compact JSON ("null" for no limit) and its
// denied_models.
func keyModelRules(t *testing.T, userID, workspace string, own any) (string, []string) {
	t.Helper()

	var ownJSON any
	if own != nil {
		ownJSON = mustJSON(own)
	}

	var out string
	if err := GetTestDB(t).QueryRow(`SELECT api.get_api_key_model_rules($1::uuid, $2, $3::jsonb)::text`,
		userID, workspace, ownJSON).Scan(&out); err != nil {
		t.Fatalf("get_api_key_model_rules failed: %v", err)
	}

	var rules struct {
		Allowed any      `json:"allowed_models"`
		Denied  []string `json:"denied_models"`
	}
	if err := json.Unmarshal([]byte(out), &rules); err != nil {
		t.Fatalf("decoding %s: %v", out, err)
	}

	if rules.Denied == nil {
		t.Fatalf("denied_models missing or null in %s", out)
	}

	return mustJSON(rules.Allowed), rules.Denied
}

// keyAllowedModels is the allowed_models part of keyModelRules.
func keyAllowedModels(t *testing.T, userID, workspace string, own any) string {
	t.Helper()

	allowed, _ := keyModelRules(t, userID, workspace, own)

	return allowed
}

func wantModels(names ...string) string {
	return mustJSON(gpModels(names...))
}

func addToTeam(t *testing.T, f *orgFixture, team *v1.Team, userID string) {
	t.Helper()

	if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: team.ID, UserID: userID}); err != nil {
		t.Fatalf("AddTeamMember failed: %v", err)
	}
}

func putInOrgUnit(t *testing.T, f *orgFixture, unit *v1.OrgUnit, userID string) {
	t.Helper()

	if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: userID, OrgUnitID: unit.ID, IdentitySource: f.source}); err != nil {
		t.Fatalf("SetOrgUnitMember failed: %v", err)
	}
}

func TestGroupPolicy_Merge(t *testing.T) {
	f := newOrgFixture(t)

	t.Run("OrgUnit levels intersect; a level without allow sets no limit", func(t *testing.T) {
		root := f.createOrgUnit("")
		mid := f.createOrgUnit(root.Metadata.Name)
		leaf := f.createOrgUnit(mid.Metadata.Name)
		user := createGoTrueTestUser(t, "gp-chain", "password123")
		putInOrgUnit(t, f, leaf, user.ID)

		setGroupPolicy(t, "OrgUnit", root.Metadata.Name, gpModels("a", "b", "c"), nil)
		setGroupPolicy(t, "OrgUnit", mid.Metadata.Name, nil, nil)
		setGroupPolicy(t, "OrgUnit", leaf.Metadata.Name, gpModels("b", "c", "d"), nil)

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("b", "c") {
			t.Errorf("allowed = %s, want b, c", got)
		}
	})

	t.Run("Teams unite, then intersect with the OrgUnits", func(t *testing.T) {
		unit := f.createOrgUnit("")
		t1, t2, t3 := f.createTeam(), f.createTeam(), f.createTeam()
		user := createGoTrueTestUser(t, "gp-teams", "password123")

		for _, team := range []*v1.Team{t1, t2, t3} {
			addToTeam(t, f, team, user.ID)
		}

		setGroupPolicy(t, "Team", t1.Metadata.Name, gpModels("a"), nil)
		setGroupPolicy(t, "Team", t2.Metadata.Name, gpModels("b", "c"), nil)
		// A Team without allow does not lift the others' limit.
		setGroupPolicy(t, "Team", t3.Metadata.Name, nil, nil)

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("a", "b", "c") {
			t.Errorf("Teams only: allowed = %s, want a, b, c", got)
		}

		putInOrgUnit(t, f, unit, user.ID)
		setGroupPolicy(t, "OrgUnit", unit.Metadata.Name, gpModels("b", "c", "x"), nil)

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("b", "c") {
			t.Errorf("with the OrgUnit: allowed = %s, want b, c", got)
		}
	})

	t.Run("deny wins", func(t *testing.T) {
		unit := f.createOrgUnit("")
		team := f.createTeam()
		user := createGoTrueTestUser(t, "gp-deny", "password123")
		putInOrgUnit(t, f, unit, user.ID)
		addToTeam(t, f, team, user.ID)

		setGroupPolicy(t, "OrgUnit", unit.Metadata.Name, gpModels("a", "b"), nil)
		setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("a", "b"), gpModels("a"))

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("b") {
			t.Errorf("allowed = %s, want b", got)
		}

		own := []map[string]any{{"model": "a"}, {"model": "b", "type": "internal", "endpoint_name": "ep-b"}}
		if got, want := keyAllowedModels(t, user.ID, gpWorkspace, own), mustJSON(own[1:]); got != want {
			t.Errorf("key list: allowed = %s, want %s", got, want)
		}
	})

	t.Run("deny alone sets no allowlist and sends the deny list", func(t *testing.T) {
		team := f.createTeam()
		user := createGoTrueTestUser(t, "gp-deny-only", "password123")
		addToTeam(t, f, team, user.ID)
		setGroupPolicy(t, "Team", team.Metadata.Name, nil, gpModels("m2", "m1"))

		// No enumeration of workspace models: a pass-through endpoint serves
		// models no list knows, and they must stay reachable.
		allowed, denied := keyModelRules(t, user.ID, gpWorkspace, nil)
		if allowed != "null" || !reflect.DeepEqual(denied, []string{"m1", "m2"}) {
			t.Errorf("rules = %s, %v; want null, [m1 m2]", allowed, denied)
		}

		own := []map[string]any{{"model": "m1"}, {"model": "m3"}}
		allowed, denied = keyModelRules(t, user.ID, gpWorkspace, own)
		if want := mustJSON(own[1:]); allowed != want || !reflect.DeepEqual(denied, []string{"m1", "m2"}) {
			t.Errorf("key list: rules = %s, %v; want %s, [m1 m2]", allowed, denied, want)
		}
	})

	t.Run("allow and deny together", func(t *testing.T) {
		team := f.createTeam()
		user := createGoTrueTestUser(t, "gp-allow-deny", "password123")
		addToTeam(t, f, team, user.ID)
		setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("a", "b"), gpModels("b", "c"))

		allowed, denied := keyModelRules(t, user.ID, gpWorkspace, nil)
		if allowed != wantModels("a") || !reflect.DeepEqual(denied, []string{"b", "c"}) {
			t.Errorf("rules = %s, %v; want [a], [b c]", allowed, denied)
		}
	})

	t.Run("no rule: no allowlist and an empty deny list", func(t *testing.T) {
		user := createGoTrueTestUser(t, "gp-norule", "password123")

		allowed, denied := keyModelRules(t, user.ID, gpWorkspace, nil)
		if allowed != "null" || len(denied) != 0 {
			t.Errorf("rules = %s, %v; want null, []", allowed, denied)
		}
	})

	t.Run("the key list and the group list", func(t *testing.T) {
		team := f.createTeam()
		user := createGoTrueTestUser(t, "gp-key", "password123")
		addToTeam(t, f, team, user.ID)

		own := []map[string]any{{"model": "a", "token_limit": 100}, {"model": "z"}}

		if got, want := keyAllowedModels(t, user.ID, gpWorkspace, own), mustJSON(own); got != want {
			t.Errorf("no policy: allowed = %s, want the key's own %s", got, want)
		}

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != "null" {
			t.Errorf("no list on either side: allowed = %s, want null", got)
		}

		setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("a", "b"), nil)

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("a", "b") {
			t.Errorf("empty key: allowed = %s, want the group's a, b", got)
		}

		if got, want := keyAllowedModels(t, user.ID, gpWorkspace, own), mustJSON(own[:1]); got != want {
			t.Errorf("both: allowed = %s, want %s", got, want)
		}

		if got := keyAllowedModels(t, user.ID, gpWorkspace, []map[string]any{}); got != "[]" {
			t.Errorf("deny-all key: allowed = %s, want []", got)
		}
	})

	t.Run("workspace pins", func(t *testing.T) {
		team := f.createTeam()
		user := createGoTrueTestUser(t, "gp-ws", "password123")
		addToTeam(t, f, team, user.ID)

		setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("a@"+gpWorkspace, "b@elsewhere", "c"), gpModels("c@elsewhere"))

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("a", "c") {
			t.Errorf("in %s: allowed = %s, want a, c", gpWorkspace, got)
		}

		if got := keyAllowedModels(t, user.ID, "elsewhere", nil); got != wantModels("b") {
			t.Errorf("elsewhere: allowed = %s, want b", got)
		}

		// A list pinned only elsewhere sets no rule here: the other groups decide.
		other := f.createTeam()
		addToTeam(t, f, other, user.ID)
		setGroupPolicy(t, "Team", other.Metadata.Name, gpModels("d"), nil)

		if _, err := GetTestDB(t).Exec(`UPDATE api.group_policies SET spec.allowed_models = $2::jsonb, spec.denied_models = $3::jsonb
			WHERE (spec).subject_name = $1`, team.Metadata.Name, mustJSON(gpModels("a@elsewhere")), mustJSON(gpModels("d@elsewhere"))); err != nil {
			t.Fatal(err)
		}

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("d") {
			t.Errorf("lists pinned elsewhere: allowed = %s, want d", got)
		}

		if got := keyAllowedModels(t, user.ID, "elsewhere", nil); got != wantModels("a") {
			t.Errorf("elsewhere: allowed = %s, want a", got)
		}
	})

	t.Run("Inactive groups still constrain", func(t *testing.T) {
		parent := f.createOrgUnit("")
		child := f.createOrgUnit(parent.Metadata.Name)
		team := f.createTeam()
		user := createGoTrueTestUser(t, "gp-inactive", "password123")
		putInOrgUnit(t, f, child, user.ID)
		addToTeam(t, f, team, user.ID)

		setGroupPolicy(t, "OrgUnit", parent.Metadata.Name, gpModels("a", "b", "c"), nil)
		setGroupPolicy(t, "Team", team.Metadata.Name, nil, gpModels("a"))

		if err := f.st.UpdateTeam(team.GetID(), &v1.Team{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
			t.Fatal(err)
		}

		if err := f.st.UpdateOrgUnit(child.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
			t.Fatal(err)
		}

		if err := f.st.UpdateOrgUnit(parent.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
			t.Fatal(err)
		}

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != wantModels("b", "c") {
			t.Errorf("allowed = %s, want b, c", got)
		}
	})

	t.Run("a user in no group", func(t *testing.T) {
		user := createGoTrueTestUser(t, "gp-nogroup", "password123")

		if got := keyAllowedModels(t, user.ID, gpWorkspace, nil); got != "null" {
			t.Errorf("no key list: allowed = %s, want null", got)
		}

		own := []map[string]any{{"model": "a"}}
		if got, want := keyAllowedModels(t, user.ID, gpWorkspace, own), mustJSON(own); got != want {
			t.Errorf("key list: allowed = %s, want %s", got, want)
		}
	})
}

// Inactive grants nothing: the role assignment side of api.user_groups.
func TestGroupPolicy_UserGroupsActive(t *testing.T) {
	f := newOrgFixture(t)
	parent := f.createOrgUnit("")
	child := f.createOrgUnit(parent.Metadata.Name)
	team := f.createTeam()
	user := createGoTrueTestUser(t, "gp-groups", "password123")
	putInOrgUnit(t, f, child, user.ID)
	addToTeam(t, f, team, user.ID)

	groups := func() map[string]bool {
		rows, err := GetTestDB(t).Query(`SELECT kind || ':' || name, active FROM api.user_groups($1::uuid)`, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		out := map[string]bool{}

		for rows.Next() {
			var k string

			var active bool
			if err := rows.Scan(&k, &active); err != nil {
				t.Fatal(err)
			}

			out[k] = active
		}

		return out
	}

	want := map[string]bool{
		"OrgUnit:" + parent.Metadata.Name: true,
		"OrgUnit:" + child.Metadata.Name:  true,
		"Team:" + team.Metadata.Name:      true,
	}
	if got := groups(); !reflect.DeepEqual(got, want) {
		t.Fatalf("user_groups = %v, want %v", got, want)
	}

	if err := f.st.UpdateOrgUnit(child.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatal(err)
	}

	// The primary OrgUnit is Inactive: the whole path stops granting.
	want["OrgUnit:"+parent.Metadata.Name] = false
	want["OrgUnit:"+child.Metadata.Name] = false

	if got := groups(); !reflect.DeepEqual(got, want) {
		t.Fatalf("user_groups = %v, want %v", got, want)
	}
}

func groupPolicyBody(kind, subject string, allowed, denied any) string {
	return mustJSON(map[string]any{
		"api_version": "v1",
		"kind":        "GroupPolicy",
		"metadata":    map[string]any{},
		"spec": map[string]any{
			"subject_kind":   kind,
			"subject_name":   subject,
			"allowed_models": allowed,
			"denied_models":  denied,
		},
	})
}

func TestGroupPolicy_Write(t *testing.T) {
	admin := adminToken(t)
	team := newOrgName("gp-t")
	teamID := createManualTeam(t, admin, team)

	code, raw := postgrestAs(t, admin, http.MethodPost, "/group_policies",
		groupPolicyBody("Team", team, []any{map[string]any{"model": "a"}, map[string]any{"model": "a"}, map[string]any{"model": "b", "workspace": ""}}, []any{}))
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

	t.Run("stored normalized, named after the group", func(t *testing.T) {
		p := created[0]
		if p.Metadata.Name != "team-"+team {
			t.Errorf("metadata.name = %q", p.Metadata.Name)
		}

		if !reflect.DeepEqual(p.Spec.AllowedModels, []v1.GroupPolicyModel{{Model: "a"}, {Model: "b"}}) || p.Spec.DeniedModels != nil {
			t.Errorf("spec = %+v", p.Spec)
		}
	})

	t.Run("a PATCH without subject keeps it", func(t *testing.T) {
		body := mustJSON(map[string]any{"spec": map[string]any{"denied_models": []any{map[string]any{"model": "c"}}}})
		code, raw := postgrestAs(t, admin, http.MethodPatch, fmt.Sprintf("/group_policies?id=eq.%d", id), body)

		var rows []v1.GroupPolicy
		if code != http.StatusOK || json.Unmarshal([]byte(raw), &rows) != nil || len(rows) != 1 {
			t.Fatalf("PATCH = HTTP %d: %s", code, raw)
		}

		if s := rows[0].Spec; s.SubjectKind != "Team" || s.SubjectName != team || s.AllowedModels != nil ||
			!reflect.DeepEqual(s.DeniedModels, []v1.GroupPolicyModel{{Model: "c"}}) {
			t.Errorf("spec after PATCH = %+v", s)
		}
	})

	errorCases := []struct {
		name, method, path, body, code string
		status                         int
	}{
		{"second policy for the group", http.MethodPost, "/group_policies", groupPolicyBody("Team", team, nil, nil), "23505", http.StatusConflict},
		{"unknown kind", http.MethodPost, "/group_policies", groupPolicyBody("Bogus", team, nil, nil), "10300", http.StatusBadRequest},
		{"unknown group", http.MethodPost, "/group_policies", groupPolicyBody("OrgUnit", team, nil, nil), "10294", http.StatusBadRequest},
		{"bad entry", http.MethodPost, "/group_policies", groupPolicyBody("Team", team, []any{"a"}, nil), "10302", http.StatusBadRequest},
		{"bad list", http.MethodPost, "/group_policies", groupPolicyBody("Team", team, nil, map[string]any{"model": "a"}), "10302", http.StatusBadRequest},
		{"subject change", http.MethodPatch, fmt.Sprintf("/group_policies?id=eq.%d", id),
			mustJSON(map[string]any{"spec": map[string]any{"subject_kind": "Team", "subject_name": "other"}}), "10301", http.StatusBadRequest},
		{"workspace", http.MethodPost, "/group_policies",
			strings.Replace(groupPolicyBody("Team", team, nil, nil), `"metadata":{}`, `"metadata":{"workspace":"default"}`, 1), "10299", http.StatusBadRequest},
	}

	for _, c := range errorCases {
		t.Run(c.name, func(t *testing.T) {
			code, raw := postgrestAs(t, admin, c.method, c.path, c.body)
			if code != c.status || !strings.Contains(raw, c.code) {
				t.Fatalf("HTTP %d: %s; want %d %s", code, raw, c.status, c.code)
			}
		})
	}

	t.Run("global group_policy permissions only", func(t *testing.T) {
		reader := userTokenWithPermissions(t, "gp-reader", []string{"group_policy:read", "team:read"})

		code, raw := postgrestAs(t, reader, http.MethodGet, fmt.Sprintf("/group_policies?id=eq.%d", id), "")
		if code != http.StatusOK || !strings.Contains(raw, team) {
			t.Fatalf("GET as reader = HTTP %d: %s", code, raw)
		}

		code, raw = postgrestAs(t, reader, http.MethodPatch, fmt.Sprintf("/group_policies?id=eq.%d", id),
			mustJSON(map[string]any{"spec": map[string]any{"allowed_models": []any{map[string]any{"model": "x"}}}}))
		if code != http.StatusOK || raw != "[]" {
			t.Fatalf("PATCH as reader = HTTP %d: %s; want no row changed", code, raw)
		}

		nobody := userTokenWithPermissions(t, "gp-nobody", []string{"team:read"})

		code, raw = postgrestAs(t, nobody, http.MethodGet, fmt.Sprintf("/group_policies?id=eq.%d", id), "")
		if code != http.StatusOK || raw != "[]" {
			t.Fatalf("GET without permission = HTTP %d: %s; want []", code, raw)
		}

		code, raw = postgrestAs(t, nobody, http.MethodPost, "/group_policies", groupPolicyBody("Team", team, nil, nil))
		if code != http.StatusForbidden {
			t.Fatalf("POST without permission = HTTP %d: %s; want 403", code, raw)
		}
	})

	t.Run("deleting the Team deletes its policy", func(t *testing.T) {
		if code, raw := postgrestAs(t, admin, http.MethodDelete, fmt.Sprintf("/teams?id=eq.%d", teamID), ""); code != http.StatusOK {
			t.Fatalf("DELETE the Team = HTTP %d: %s", code, raw)
		}

		var n int
		if err := GetTestDB(t).QueryRow(`SELECT count(*) FROM api.group_policies WHERE id = $1`, id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("policies left = %d, %v", n, err)
		}
	})
}

func TestGroupPolicy_GetUserGroupPolicy(t *testing.T) {
	f := newOrgFixture(t)
	team := f.createTeam()
	user := createGoTrueTestUser(t, "gp-rpc", "password123")
	addToTeam(t, f, team, user.ID)
	setGroupPolicy(t, "Team", team.Metadata.Name, gpModels("a", "b"), gpModels("b"))

	if _, err := GetTestDB(t).Exec(`UPDATE api.teams SET metadata.display_name = 'Team Shown' WHERE id = $1`, team.ID); err != nil {
		t.Fatal(err)
	}

	call := func(token string) (int, string) {
		return postgrestAs(t, token, http.MethodPost, "/rpc/get_user_group_policy",
			mustJSON(map[string]any{"p_user_id": user.ID, "p_workspace": gpWorkspace}))
	}

	self := gotruePasswordLogin(t, user.Email, "password123").AccessToken

	for name, token := range map[string]string{"self": self, "admin": adminToken(t)} {
		code, raw := call(token)
		if code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", name, code, raw)
		}

		var got struct {
			AllowedModels []string `json:"allowed_models"`
			DeniedModels  []string `json:"denied_models"`
			Groups        []struct {
				Kind, Name  string
				DisplayName string `json:"display_name"`
				Active      bool
			} `json:"groups"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}

		// The member cannot read the teams table, so the display name comes with the policy.
		if !reflect.DeepEqual(got.AllowedModels, []string{"a"}) || !reflect.DeepEqual(got.DeniedModels, []string{"b"}) ||
			len(got.Groups) != 1 || got.Groups[0].Name != team.Metadata.Name || !got.Groups[0].Active ||
			got.Groups[0].DisplayName != "Team Shown" {
			t.Errorf("%s: %s", name, raw)
		}
	}

	other := userTokenWithPermissions(t, "gp-other", []string{"team:read"})
	if code, raw := call(other); code != http.StatusForbidden {
		t.Errorf("another user = HTTP %d: %s; want 403", code, raw)
	}

	t.Run("no workspace: groups only", func(t *testing.T) {
		code, raw := postgrestAs(t, self, http.MethodPost, "/rpc/get_user_group_policy",
			mustJSON(map[string]any{"p_user_id": user.ID, "p_workspace": nil}))
		if code != http.StatusOK {
			t.Fatalf("HTTP %d: %s", code, raw)
		}

		var got struct {
			AllowedModels []string         `json:"allowed_models"`
			DeniedModels  []string         `json:"denied_models"`
			Groups        []map[string]any `json:"groups"`
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}

		if got.AllowedModels != nil || got.DeniedModels != nil || len(got.Groups) != 1 {
			t.Errorf("got %s; want no merged lists and one group", raw)
		}

		code, raw = postgrestAs(t, self, http.MethodPost, "/rpc/get_user_group_policy",
			mustJSON(map[string]any{"p_user_id": user.ID, "p_workspace": " "}))
		if code != http.StatusBadRequest {
			t.Errorf("blank workspace = HTTP %d: %s; want 400", code, raw)
		}
	})

	code, raw := postgrestAs(t, adminToken(t), http.MethodPost, "/rpc/get_api_key_model_rules",
		mustJSON(map[string]any{"p_owner_id": user.ID, "p_workspace": gpWorkspace, "p_allowed_models": nil}))
	if code == http.StatusOK {
		t.Errorf("get_api_key_model_rules as a user = HTTP %d: %s; want refused", code, raw)
	}
}

func TestGroupPolicy_PresetRolePermissions(t *testing.T) {
	db := GetTestDB(t)

	for _, p := range []string{"group_policy:read", "group_policy:create", "group_policy:update", "group_policy:delete"} {
		var admin, dept, ws bool
		if err := db.QueryRow(`
			SELECT
				bool_or((metadata).name = 'admin' AND $1::api.permission_action = ANY((spec).permissions)),
				bool_or((metadata).name = 'department-admin' AND $1::api.permission_action = ANY((spec).permissions)),
				bool_or((metadata).name = 'workspace-user' AND $1::api.permission_action = ANY((spec).permissions))
			FROM api.roles`, p).Scan(&admin, &dept, &ws); err != nil {
			t.Fatalf("reading roles failed: %v", err)
		}

		// department-admin writes the user policies of its subtree.
		if !admin || !dept || ws {
			t.Errorf("%s: admin %v, department-admin %v, workspace-user %v", p, admin, dept, ws)
		}
	}
}
