package dbtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/identitysync"
	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Tests for Teams created in Neutree (manual Teams) next to synced
// ones.

var teamWritePermissions = []string{"team:create", "team:update", "team:delete", "team:assign-member"}

func manualTeamBody(name string) string {
	return mustJSON(map[string]any{
		"api_version": "v1",
		"kind":        "Team",
		"metadata":    map[string]any{"name": name, "display_name": "Team " + name},
	})
}

// createManualTeam creates a manual Team through PostgREST as the given user
// and returns its id.
func createManualTeam(t *testing.T, token, name string) int {
	t.Helper()

	code, raw := postgrestAs(t, token, http.MethodPost, "/teams", manualTeamBody(name))
	if code != http.StatusCreated {
		t.Fatalf("POST /teams %s = HTTP %d: %s", name, code, raw)
	}

	var rows []v1.Team
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("POST /teams returned %s: %v", raw, err)
	}

	t.Cleanup(func() {
		_, _ = GetTestDB(t).Exec(`DELETE FROM api.teams WHERE id = $1`, rows[0].ID)
	})

	return rows[0].ID
}

func teamMemberIDs(t *testing.T, teamID int) []string {
	t.Helper()

	rows, err := GetTestDB(t).Query(`SELECT user_id::text FROM api.team_members WHERE team_id = $1 ORDER BY user_id`, teamID)
	if err != nil {
		t.Fatalf("listing members of %d failed: %v", teamID, err)
	}
	defer rows.Close()

	var ids []string

	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan failed: %v", err)
		}

		ids = append(ids, id)
	}

	return ids
}

func getTeam(st storage.Storage, id string) (*v1.Team, error) {
	teams, err := st.ListTeam(storage.ListOption{Filters: []storage.Filter{{Column: "id", Operator: "eq", Value: id}}})
	if err != nil {
		return nil, err
	}

	if len(teams) != 1 {
		return nil, fmt.Errorf("found %d Teams with id %s", len(teams), id)
	}

	return &teams[0], nil
}

// tokenWithPresetRole logs in a new user holding the preset role globally.
func tokenWithPresetRole(t *testing.T, prefix, role string) string {
	t.Helper()
	waitForPostgREST(t)

	user := createGoTrueTestUser(t, prefix, "password123")
	db := GetTestDB(t)
	name := newOrgName(prefix)

	if _, err := db.Exec(`
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES ('v1', 'RoleAssignment',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, NULL, TRUE, $3, NULL)::api.role_assignment_spec)`, name, user.ID, role); err != nil {
		t.Fatalf("assigning %s failed: %v", role, err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (metadata).name = $1`, name)
	})

	return gotruePasswordLogin(t, user.Email, "password123").AccessToken
}

func TestManualTeam_Write(t *testing.T) {
	f := newOrgFixture(t)
	synced := f.createTeam()

	local := createGoTrueTestUser(t, "mteam-local", "password123")
	other := createGoTrueTestUser(t, "mteam-other", "password123")

	external := createLDAPShapedUser(t, newExternalID())
	t.Cleanup(func() { deleteGoTrueUser(t, external) })

	if err := f.st.CreateExternalIdentity(&storage.ExternalIdentity{Source: "ldap:" + f.source, ExternalID: newExternalID(), UserID: external}); err != nil {
		t.Fatalf("CreateExternalIdentity failed: %v", err)
	}

	if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: synced.ID, UserID: local.ID}); err != nil {
		t.Fatalf("sync adding a member failed: %v", err)
	}

	admin := adminToken(t)
	writer := userTokenWithPermissions(t, "mteam-writer", append([]string{"team:read"}, teamWritePermissions...))

	t.Run("a global writer manages a manual Team and its members", func(t *testing.T) {
		for _, token := range []string{admin, writer} {
			id := createManualTeam(t, token, newOrgName("mt"))

			var source, externalID, phase *string
			if err := GetTestDB(t).QueryRow(`SELECT (spec).identity_source, (spec).external_id, (status).phase FROM api.teams WHERE id = $1`, id).
				Scan(&source, &externalID, &phase); err != nil || source != nil || externalID != nil || phase == nil || *phase != "Active" {
				t.Fatalf("stored manual Team = %v %v %v, %v; want no source and Active", source, externalID, phase, err)
			}

			// Local and external accounts alike.
			for _, user := range []string{local.ID, external} {
				body := mustJSON(map[string]any{"team_id": id, "user_id": user})
				if code, raw := postgrestAs(t, token, http.MethodPost, "/team_members", body); code != http.StatusCreated {
					t.Fatalf("POST /team_members %s = HTTP %d: %s", user, code, raw)
				}
			}

			patch := mustJSON(map[string]any{"metadata": map[string]any{"name": "", "display_name": "Renamed"}})
			if code, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", id), patch); code != http.StatusBadRequest || !strings.Contains(raw, "10273") {
				t.Errorf("renaming metadata.name = HTTP %d, want 400 10273: %s", code, raw)
			}

			code, raw := postgrestAs(t, token, http.MethodGet, fmt.Sprintf("/teams?id=eq.%d", id), "")
			var got []v1.Team
			if code != http.StatusOK || json.Unmarshal([]byte(raw), &got) != nil || len(got) != 1 {
				t.Fatalf("GET /teams = HTTP %d: %s", code, raw)
			}

			meta := *got[0].Metadata
			meta.DisplayName = "Renamed"
			patch = mustJSON(map[string]any{"metadata": meta})
			if code, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", id), patch); code != http.StatusOK || !strings.Contains(raw, "Renamed") {
				t.Errorf("PATCH display_name = HTTP %d: %s", code, raw)
			}

			// Inactive is for synced Teams only.
			patch = mustJSON(map[string]any{"status": map[string]any{"phase": "Inactive"}})
			if code, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", id), patch); code != http.StatusBadRequest || !strings.Contains(raw, "10291") {
				t.Errorf("PATCH phase Inactive = HTTP %d, want 400 10291: %s", code, raw)
			}

			patch = mustJSON(map[string]any{"status": map[string]any{"phase": "Active"}})
			if code, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", id), patch); code != http.StatusOK || !strings.Contains(raw, `"id"`) {
				t.Errorf("PATCH phase Active = HTTP %d: %s", code, raw)
			}

			path := fmt.Sprintf("/team_members?team_id=eq.%d&user_id=eq.%s", id, external)
			if code, raw := postgrestAs(t, token, http.MethodDelete, path, ""); code != http.StatusOK || !strings.Contains(raw, external) {
				t.Errorf("DELETE /team_members = HTTP %d: %s", code, raw)
			}

			if code, raw := postgrestAs(t, token, http.MethodDelete, fmt.Sprintf("/teams?id=eq.%d", id), ""); code != http.StatusOK || !strings.Contains(raw, `"id"`) {
				t.Fatalf("DELETE /teams = HTTP %d: %s", code, raw)
			}

			if ids := teamMemberIDs(t, id); len(ids) != 0 {
				t.Errorf("members after deleting the Team = %v, want none", ids)
			}
		}
	})

	t.Run("names", func(t *testing.T) {
		code, raw := postgrestAs(t, admin, http.MethodPost, "/teams", manualTeamBody(f.source+"-0123456789ab"))
		if code != http.StatusBadRequest || !strings.Contains(raw, "10288") {
			t.Errorf("POST a sync-shaped name = HTTP %d, want 400 10288: %s", code, raw)
		}

		code, raw = postgrestAs(t, admin, http.MethodPost, "/teams", manualTeamBody(synced.Metadata.Name))
		if code != http.StatusConflict {
			t.Errorf("POST the name of a synced Team = HTTP %d, want 409: %s", code, raw)
		}

		name := newOrgName("mt")
		createManualTeam(t, admin, name)

		if code, raw := postgrestAs(t, admin, http.MethodPost, "/teams", manualTeamBody(name)); code != http.StatusConflict {
			t.Errorf("POST a taken name = HTTP %d, want 409: %s", code, raw)
		}

		body := mustJSON(map[string]any{
			"api_version": "v1", "kind": "Team",
			"metadata": map[string]any{"name": newOrgName("mt")},
			"status":   map[string]any{"phase": "Inactive"},
		})
		if code, raw := postgrestAs(t, admin, http.MethodPost, "/teams", body); code != http.StatusBadRequest || !strings.Contains(raw, "10291") {
			t.Errorf("POST an Inactive Team = HTTP %d, want 400 10291: %s", code, raw)
		}

		body = mustJSON(map[string]any{
			"api_version": "v1", "kind": "Team",
			"metadata": map[string]any{"name": newOrgName("mt"), "workspace": "default"},
		})
		if code, raw := postgrestAs(t, admin, http.MethodPost, "/teams", body); code != http.StatusBadRequest || !strings.Contains(raw, "10270") {
			t.Errorf("POST a Team in a workspace = HTTP %d, want 400 10270: %s", code, raw)
		}
	})

	t.Run("synced Teams and their members are read-only", func(t *testing.T) {
		body := mustJSON(map[string]any{
			"api_version": "v1", "kind": "Team",
			"metadata": map[string]any{"name": newOrgName("mt")},
			"spec":     map[string]any{"identity_source": f.source, "external_id": "claimed"},
		})
		if code, raw := postgrestAs(t, admin, http.MethodPost, "/teams", body); code != http.StatusForbidden || !strings.Contains(raw, "10289") {
			t.Errorf("POST a Team claiming a source = HTTP %d, want 403 10289: %s", code, raw)
		}

		patch := mustJSON(map[string]any{"status": map[string]any{"phase": "Inactive"}})
		if code, raw := postgrestAs(t, admin, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", synced.ID), patch); code != http.StatusForbidden || !strings.Contains(raw, "10289") {
			t.Errorf("PATCH a synced Team = HTTP %d, want 403 10289: %s", code, raw)
		}

		if code, raw := postgrestAs(t, admin, http.MethodDelete, fmt.Sprintf("/teams?id=eq.%d", synced.ID), ""); code != http.StatusForbidden || !strings.Contains(raw, "10289") {
			t.Errorf("DELETE a synced Team = HTTP %d, want 403 10289: %s", code, raw)
		}

		// A manual Team cannot be turned into a synced one either.
		id := createManualTeam(t, admin, newOrgName("mt"))
		patch = mustJSON(map[string]any{"spec": map[string]any{"identity_source": f.source, "external_id": "claimed"}})
		if code, raw := postgrestAs(t, admin, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", id), patch); code != http.StatusForbidden || !strings.Contains(raw, "10289") {
			t.Errorf("PATCH a manual Team to a source = HTTP %d, want 403 10289: %s", code, raw)
		}

		body = mustJSON(map[string]any{"team_id": synced.ID, "user_id": other.ID})
		if code, raw := postgrestAs(t, admin, http.MethodPost, "/team_members", body); code != http.StatusForbidden || !strings.Contains(raw, "10290") {
			t.Errorf("POST a member of a synced Team = HTTP %d, want 403 10290: %s", code, raw)
		}

		path := fmt.Sprintf("/team_members?team_id=eq.%d&user_id=eq.%s", synced.ID, local.ID)
		if code, raw := postgrestAs(t, admin, http.MethodDelete, path, ""); code != http.StatusForbidden || !strings.Contains(raw, "10290") {
			t.Errorf("DELETE a member of a synced Team = HTTP %d, want 403 10290: %s", code, raw)
		}

		patch = mustJSON(map[string]any{"team_id": id})
		if code, raw := postgrestAs(t, admin, http.MethodPatch, path, patch); strings.Contains(raw, local.ID) {
			t.Errorf("PATCH moved a synced membership: HTTP %d: %s", code, raw)
		}

		stored, err := getTeam(f.st, synced.GetID())
		if err != nil || !stored.IsActive() || stored.Spec.IdentitySource != f.source {
			t.Errorf("synced Team after the attempts = %+v, %v", stored, err)
		}

		if ids := teamMemberIDs(t, synced.ID); len(ids) != 1 || ids[0] != local.ID {
			t.Errorf("synced members after the attempts = %v, want only %s", ids, local.ID)
		}
	})

	t.Run("sync still writes synced Teams", func(t *testing.T) {
		spec := *synced.Spec
		meta := *synced.Metadata
		meta.DisplayName = "Synced rename"

		if err := f.st.UpdateTeam(synced.GetID(), &v1.Team{Metadata: &meta, Spec: &spec}); err != nil {
			t.Errorf("sync renaming a Team failed: %v", err)
		}

		if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: synced.ID, UserID: other.ID}); err != nil {
			t.Errorf("sync adding a member failed: %v", err)
		}

		if err := f.st.DeleteTeamMember(synced.ID, other.ID); err != nil {
			t.Errorf("sync removing a member failed: %v", err)
		}
	})

	t.Run("deleting a user still removes its synced memberships", func(t *testing.T) {
		gone := createGoTrueTestUser(t, "mteam-gone", "password123")
		if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: synced.ID, UserID: gone.ID}); err != nil {
			t.Fatalf("sync adding a member failed: %v", err)
		}

		if code, raw := postgrestAs(t, admin, http.MethodDelete, "/user_profiles?id=eq."+gone.ID, ""); code != http.StatusOK || !strings.Contains(raw, gone.ID) {
			t.Fatalf("DELETE /user_profiles = HTTP %d: %s", code, raw)
		}

		for _, id := range teamMemberIDs(t, synced.ID) {
			if id == gone.ID {
				t.Errorf("the deleted user is still a member")
			}
		}
	})

	t.Run("without the global permissions nobody writes", func(t *testing.T) {
		id := createManualTeam(t, admin, newOrgName("mt"))

		tokens := map[string]string{
			"plain user":              userTokenWithPermissions(t, "mteam-none", nil),
			"reader":                  userTokenWithPermissions(t, "mteam-reader", []string{"team:read"}),
			"department-admin":        tokenWithPresetRole(t, "mteam-dept", "department-admin"),
			"workspace-user":          tokenWithPresetRole(t, "mteam-ws", "workspace-user"),
			"assigner without create": userTokenWithPermissions(t, "mteam-assigner", []string{"team:read", "team:assign-member"}),
		}

		for who, token := range tokens {
			if code, raw := postgrestAs(t, token, http.MethodPost, "/teams", manualTeamBody(newOrgName("mt"))); code != http.StatusForbidden {
				t.Errorf("%s: POST /teams = HTTP %d, want 403: %s", who, code, raw)
			}

			patch := mustJSON(map[string]any{"status": map[string]any{"phase": "Inactive"}})
			if _, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/teams?id=eq.%d", id), patch); strings.Contains(raw, `"id"`) {
				t.Errorf("%s: PATCH /teams changed a row: %s", who, raw)
			}

			if _, raw := postgrestAs(t, token, http.MethodDelete, fmt.Sprintf("/teams?id=eq.%d", id), ""); strings.Contains(raw, `"id"`) {
				t.Errorf("%s: DELETE /teams removed a row: %s", who, raw)
			}

			if who == "assigner without create" {
				continue
			}

			body := mustJSON(map[string]any{"team_id": id, "user_id": other.ID})
			if code, raw := postgrestAs(t, token, http.MethodPost, "/team_members", body); code != http.StatusForbidden {
				t.Errorf("%s: POST /team_members = HTTP %d, want 403: %s", who, code, raw)
			}

			// Not 10290: a user without the permission learns nothing about the Team.
			body = mustJSON(map[string]any{"team_id": synced.ID, "user_id": other.ID})
			if code, raw := postgrestAs(t, token, http.MethodPost, "/team_members", body); code != http.StatusForbidden || strings.Contains(raw, "10290") {
				t.Errorf("%s: POST /team_members to a synced Team = HTTP %d, want a plain 403: %s", who, code, raw)
			}
		}

		var phase string
		if err := GetTestDB(t).QueryRow(`SELECT (status).phase FROM api.teams WHERE id = $1`, id).Scan(&phase); err != nil || phase != "Active" {
			t.Errorf("manual Team after the attempts: phase %q, %v; want Active", phase, err)
		}

		if ids := teamMemberIDs(t, id); len(ids) != 0 {
			t.Errorf("manual Team members after the attempts = %v, want none", ids)
		}
	})
}

// TestManualTeam_SyncLeavesItAlone runs the real sync around a manual Team
// whose members include a synced user: sync changes the user's synced Teams
// and deactivates a group, and the manual Team keeps its state and members.
func TestManualTeam_SyncLeavesItAlone(t *testing.T) {
	f := newOrgFixture(t)
	linkSource := auth.LinkSource(auth.LDAPSource, f.source)
	cleanupSyncedUsers(t, linkSource)

	suffix := fmt.Sprint(time.Now().UnixNano())
	id := func(s string) string { return s + "-" + suffix }

	dir := &staticDirectory{snapshot: &orgsync.Snapshot{
		Teams: []orgsync.Team{{ExternalID: id("core"), DisplayName: "Core"}},
		Users: []orgsync.User{
			{ExternalID: id("lin"), Username: "lin", DisplayName: "Lin", TeamExternalIDs: []string{id("core")}},
		},
	}}

	syncer := &identitysync.Syncer{Store: f.st, Auth: goTrueAdmin(t)}
	ctx := context.Background()

	if result, err := syncer.Run(ctx, f.source, dir); err != nil || result.Pending() != 0 {
		t.Fatalf("first sync = %+v, %v", result, err)
	}

	users, err := f.st.ListIdentitySourceSyncUsers(f.source)
	if err != nil || len(users) != 1 {
		t.Fatalf("ListIdentitySourceSyncUsers = %+v, %v", users, err)
	}

	lin := users[0].UserID
	local := createGoTrueTestUser(t, "mteam-sync-local", "password123")

	admin := adminToken(t)
	manual := createManualTeam(t, admin, newOrgName("mt"))

	for _, user := range []string{lin, local.ID} {
		body := mustJSON(map[string]any{"team_id": manual, "user_id": user})
		if code, raw := postgrestAs(t, admin, http.MethodPost, "/team_members", body); code != http.StatusCreated {
			t.Fatalf("POST /team_members = HTTP %d: %s", code, raw)
		}
	}

	if users, err := f.st.ListIdentitySourceSyncUsers(f.source); err != nil || len(users) != 1 || len(users[0].TeamExternalIDs) != 1 {
		t.Errorf("sync sees %+v (%v), want lin in its own Team only", users, err)
	}

	if result, err := syncer.Run(ctx, f.source, dir); err != nil || result.Planned != 0 {
		t.Errorf("sync with a manual Team present = %+v, %v; want an empty plan", result, err)
	}

	// lin leaves every group and core is gone from the directory.
	dir.snapshot.Users[0].TeamExternalIDs = nil
	dir.snapshot.Teams = nil

	result, err := syncer.Run(ctx, f.source, dir)
	if err != nil || result.Deactivated != 1 || result.Memberships != 1 {
		t.Fatalf("sync = %+v, %v; want core deactivated and lin removed from it", result, err)
	}

	team, err := getTeam(f.st, fmt.Sprint(manual))
	if err != nil || !team.IsActive() || team.Spec.IdentitySource != "" {
		t.Errorf("manual Team after sync = %+v, %v", team, err)
	}

	if ids := teamMemberIDs(t, manual); len(ids) != 2 {
		t.Errorf("manual Team members after sync = %v, want lin and the local user", ids)
	}
}

func TestManualTeam_PresetRolePermissions(t *testing.T) {
	db := GetTestDB(t)

	for _, p := range teamWritePermissions {
		var admin, dept, ws bool
		if err := db.QueryRow(`
			SELECT
				bool_or((metadata).name = 'admin' AND $1::api.permission_action = ANY((spec).permissions)),
				bool_or((metadata).name = 'department-admin' AND $1::api.permission_action = ANY((spec).permissions)),
				bool_or((metadata).name = 'workspace-user' AND $1::api.permission_action = ANY((spec).permissions))
			FROM api.roles`, p).Scan(&admin, &dept, &ws); err != nil {
			t.Fatalf("reading roles failed: %v", err)
		}

		if !admin || dept || ws {
			t.Errorf("%s: admin %v, department-admin %v, workspace-user %v; want only admin", p, admin, dept, ws)
		}
	}
}
