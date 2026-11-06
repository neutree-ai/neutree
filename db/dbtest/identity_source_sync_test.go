package dbtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests cover the sync settings of identity sources: the sync fields of
// spec and status, the trigger that keeps them on a write that leaves them
// out, api.request_identity_source_sync, and the sync columns of
// api.external_identities and api.list_identity_source_sync_users.

func syncSourceJSON(name string, sync, ldapSync map[string]any) string {
	ldap := map[string]any{
		"url":           "ldaps://ldap.example.org:636",
		"bind_dn":       "cn=svc,dc=example,dc=org",
		"bind_password": "secret-" + name,
		"user_base_dn":  "ou=people,dc=example,dc=org",
		"user_filter":   "(&(objectClass=inetOrgPerson)(uid={username}))",
	}
	if ldapSync != nil {
		ldap["sync"] = ldapSync
	}

	spec := map[string]any{"type": "ldap", "enabled": true, "ldap": ldap}
	if sync != nil {
		spec["sync"] = sync
	}

	return mustJSON(map[string]any{
		"api_version": "v1",
		"kind":        "IdentitySource",
		"metadata":    map[string]any{"name": name},
		"spec":        spec,
	})
}

func getIdentitySource(t *testing.T, token string, id int) v1.IdentitySource {
	t.Helper()

	code, raw := postgrestAs(t, token, http.MethodGet, fmt.Sprintf("/identity_sources?id=eq.%d", id), "")

	var rows []v1.IdentitySource
	if code != http.StatusOK || json.Unmarshal([]byte(raw), &rows) != nil || len(rows) != 1 {
		t.Fatalf("GET identity source %d = HTTP %d: %s", id, code, raw)
	}

	return rows[0]
}

func TestIdentitySourceSync_SpecDefaultsAndValidation(t *testing.T) {
	token := adminToken(t)
	name := newIdentitySourceName("sync")
	source := createIdentitySource(t, token, syncSourceJSON(name,
		map[string]any{"enabled": true},
		map[string]any{"org_unit_base_dn": " ou=org,dc=example,dc=org ", "group_base_dn": "", "page_size": 0}))

	got := getIdentitySource(t, token, source.ID)
	if got.Spec.Sync == nil || !got.Spec.Sync.Enabled || got.Spec.Sync.Interval != 3600 {
		t.Errorf("spec.sync = %+v, want enabled with the 3600s default", got.Spec.Sync)
	}

	if s := got.Spec.LDAP.Sync; s == nil || s.OrgUnitBaseDN != "ou=org,dc=example,dc=org" || s.GroupBaseDN != "" || s.PageSize != 0 {
		t.Errorf("spec.ldap.sync = %+v, want the base DN trimmed and empties dropped", s)
	}

	path := fmt.Sprintf("/identity_sources?id=eq.%d", source.ID)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"interval below a minute", syncSourceJSON(name, map[string]any{"enabled": true, "interval": 30}, nil)},
		{"negative interval", syncSourceJSON(name, map[string]any{"enabled": true, "interval": -1}, nil)},
		{"user list filter with {username}", syncSourceJSON(name, nil, map[string]any{"user_list_filter": "(uid={username})"})},
		{"negative page size", syncSourceJSON(name, nil, map[string]any{"page_size": -5})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := postgrestAs(t, token, http.MethodPatch, path, tc.body)
			if code != http.StatusBadRequest || !strings.Contains(raw, `"code":"10262"`) {
				t.Errorf("PATCH = HTTP %d, want 400 with code 10262: %s", code, raw)
			}
		})
	}
}

// A client that does not know the sync fields sends spec without them; that
// must not switch the sync off or forget where it reads from (NEU-717).
func TestIdentitySourceSync_PartialSpecKeepsSync(t *testing.T) {
	token := adminToken(t)
	name := newIdentitySourceName("keepsync")
	source := createIdentitySource(t, token, syncSourceJSON(name,
		map[string]any{"enabled": true, "interval": 600},
		map[string]any{"org_unit_base_dn": "ou=org,dc=example,dc=org", "disabled_user_filter": "(employeeType=disabled)"}))
	path := fmt.Sprintf("/identity_sources?id=eq.%d", source.ID)

	// The spec without the sync settings, and without the bind password.
	if code, raw := postgrestAs(t, token, http.MethodPatch, path, ldapSourceJSON(name, "Renamed", "", false)); code != http.StatusOK {
		t.Fatalf("PATCH = HTTP %d: %s", code, raw)
	}

	got := getIdentitySource(t, token, source.ID)
	if got.Spec.Enabled || got.Metadata.DisplayName != "Renamed" {
		t.Errorf("the rest of the write was not applied: %+v %+v", got.Metadata, got.Spec)
	}

	if got.Spec.Sync == nil || !got.Spec.Sync.Enabled || got.Spec.Sync.Interval != 600 {
		t.Errorf("spec.sync = %+v, want the stored one", got.Spec.Sync)
	}

	if s := got.Spec.LDAP.Sync; s == nil || s.OrgUnitBaseDN != "ou=org,dc=example,dc=org" || s.DisabledUserFilter != "(employeeType=disabled)" {
		t.Errorf("spec.ldap.sync = %+v, want the stored one", s)
	}

	if secret := storedSecrets(t, name).LDAPBindPassword; secret != "secret-"+name {
		t.Errorf("bind password = %q, want the stored one", secret)
	}

	// Turning sync off is an explicit write.
	if code, raw := postgrestAs(t, token, http.MethodPatch, path,
		syncSourceJSON(name, map[string]any{"enabled": false}, nil)); code != http.StatusOK {
		t.Fatalf("PATCH = HTTP %d: %s", code, raw)
	}

	if got := getIdentitySource(t, token, source.ID); got.Spec.Sync == nil || got.Spec.Sync.Enabled {
		t.Errorf("spec.sync = %+v, want disabled", got.Spec.Sync)
	}
}

func TestIdentitySourceSync_RequestSync(t *testing.T) {
	token := adminToken(t)
	name := newIdentitySourceName("reqsync")
	source := createIdentitySource(t, token, syncSourceJSON(name, map[string]any{"enabled": true}, nil))
	off := newIdentitySourceName("reqoff")
	createIdentitySource(t, token, syncSourceJSON(off, nil, nil))

	request := func(t *testing.T, token, source string) (int, string) {
		t.Helper()
		return postgrestAs(t, token, http.MethodPost, "/rpc/request_identity_source_sync", mustJSON(map[string]any{"p_name": source}))
	}

	before := time.Now().Add(-time.Minute)

	code, raw := request(t, token, name)
	if code != http.StatusOK {
		t.Fatalf("request = HTTP %d: %s", code, raw)
	}

	got := getIdentitySource(t, token, source.ID)

	requested, err := time.Parse(time.RFC3339Nano, got.Spec.Sync.RequestedAt)
	if err != nil || requested.Before(before) {
		t.Errorf("spec.sync.requested_at = %q (%v), want now", got.Spec.Sync.RequestedAt, err)
	}

	if secret := storedSecrets(t, name).LDAPBindPassword; secret != "secret-"+name {
		t.Errorf("bind password = %q, want the stored one", secret)
	}

	t.Run("sync not enabled", func(t *testing.T) {
		if code, raw := request(t, token, off); code != http.StatusBadRequest || !strings.Contains(raw, "10264") {
			t.Errorf("request = HTTP %d, want 400 with code 10264: %s", code, raw)
		}
	})

	t.Run("unknown source", func(t *testing.T) {
		if code, raw := request(t, token, "no-such-source"); code != http.StatusNotFound {
			t.Errorf("request = HTTP %d, want 404: %s", code, raw)
		}
	})

	t.Run("read permission only", func(t *testing.T) {
		reader := userTokenWithPermissions(t, "sync-reader", []string{"identity_source:read"})
		if code, raw := request(t, reader, name); code != http.StatusForbidden {
			t.Errorf("request = HTTP %d, want 403: %s", code, raw)
		}
	})

	t.Run("no permission", func(t *testing.T) {
		nobody := userTokenWithPermissions(t, "sync-nobody", nil)
		if code, raw := request(t, nobody, name); code != http.StatusNotFound {
			t.Errorf("request = HTTP %d, want 404: %s", code, raw)
		}
	})
}

func TestIdentitySourceSync_StatusRoundTrip(t *testing.T) {
	token := adminToken(t)
	name := newIdentitySourceName("syncstat")
	source := createIdentitySource(t, token, syncSourceJSON(name, map[string]any{"enabled": true}, nil))

	status := &v1.IdentitySourceStatus{
		Phase: v1.IdentitySourcePhaseCONNECTED,
		LastSync: &v1.IdentitySourceSyncStatus{
			StartedAt:   "2026-10-09T12:00:00Z",
			FinishedAt:  "2026-10-09T12:00:05Z",
			OK:          false,
			Message:     "create team x: boom",
			Created:     3,
			Memberships: 2,
			Pending:     4,
		},
	}

	if err := NewTestStorage(t).UpdateIdentitySource(fmt.Sprint(source.ID), &v1.IdentitySource{Status: status}); err != nil {
		t.Fatalf("status update failed: %v", err)
	}

	got := getIdentitySource(t, token, source.ID).Status
	if got == nil || got.LastSync == nil || got.LastSync.Created != 3 || got.LastSync.Pending != 4 ||
		got.LastSync.Message != "create team x: boom" || got.LastSync.OK {
		t.Errorf("status = %+v / %+v", got, got.LastSync)
	}
}

func TestIdentitySourceSync_ExternalIdentityColumns(t *testing.T) {
	st := NewTestStorage(t)

	extID := newExternalID()
	userID := createLDAPShapedUser(t, extID)
	t.Cleanup(func() { deleteGoTrueUser(t, userID) })

	link := &storage.ExternalIdentity{Source: "ldap:cols", ExternalID: extID, UserID: userID, Username: "u", Email: "u@example.org"}
	if err := st.CreateExternalIdentity(link); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if err := st.UpdateExternalIdentitySync(&storage.ExternalIdentity{
		Source: "ldap:cols", ExternalID: extID, Username: "u2", SyncDeactivated: storage.SyncDeactivatedKept,
	}); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	var username, marker string

	var email *string
	if err := GetTestDB(t).QueryRow(`SELECT username, email, sync_deactivated FROM api.external_identities WHERE source = 'ldap:cols' AND external_id = $1`,
		extID).Scan(&username, &email, &marker); err != nil {
		t.Fatalf("read back failed: %v", err)
	}

	if username != "u2" || email != nil || marker != storage.SyncDeactivatedKept {
		t.Errorf("link = %q, %v, %q; want u2, NULL, kept", username, email, marker)
	}

	if _, err := GetTestDB(t).Exec(`UPDATE api.external_identities SET sync_deactivated = 'other' WHERE external_id = $1`, extID); err == nil {
		t.Error("an unknown sync_deactivated value was accepted")
	}

	// The sync state of users is not for users to read.
	token := adminToken(t)
	if code, raw := postgrestAs(t, token, http.MethodPost, "/rpc/list_identity_source_sync_users",
		`{"p_identity_source":"cols"}`); code != http.StatusUnauthorized && code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("list_identity_source_sync_users as admin = HTTP %d, want denied: %s", code, raw)
	}
}

func TestIdentitySourceSync_ListSyncUsers(t *testing.T) {
	f := newOrgFixture(t)
	other := newOrgFixture(t)
	unit := f.createOrgUnit("")
	teamA, teamB, otherTeam := f.createTeam(), f.createTeam(), other.createTeam()

	link := func(source, suffix string) (string, string) {
		t.Helper()

		extID := newExternalID() + "-" + suffix
		userID := createLDAPShapedUser(t, extID)
		t.Cleanup(func() { deleteGoTrueUser(t, userID) })

		if err := f.st.CreateExternalIdentity(&storage.ExternalIdentity{Source: source, ExternalID: extID, UserID: userID}); err != nil {
			t.Fatalf("link %s failed: %v", extID, err)
		}

		return extID, userID
	}

	memberExt, member := link("ldap:"+f.source, "member")
	loneExt, _ := link("ldap:"+f.source, "lone")
	link("ldap:"+other.source, "other")

	if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: member, OrgUnitID: unit.ID, IdentitySource: f.source}); err != nil {
		t.Fatalf("SetOrgUnitMember failed: %v", err)
	}

	for _, team := range []*v1.Team{teamB, otherTeam, teamA} {
		if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: team.ID, UserID: member}); err != nil {
			t.Fatalf("AddTeamMember(%s) failed: %v", team.Metadata.Name, err)
		}
	}

	users, err := f.st.ListIdentitySourceSyncUsers(f.source)
	if err != nil {
		t.Fatalf("ListIdentitySourceSyncUsers failed: %v", err)
	}

	got := map[string]storage.IdentitySourceSyncUser{}
	for _, u := range users {
		got[u.ExternalID] = u
	}

	if len(users) != 2 || got[memberExt].UserID != member || got[loneExt].ExternalID == "" {
		t.Fatalf("users = %+v, want the two linked under ldap:%s", users, f.source)
	}

	wantTeams := []string{teamA.Spec.ExternalID, teamB.Spec.ExternalID}
	if wantTeams[0] > wantTeams[1] {
		wantTeams[0], wantTeams[1] = wantTeams[1], wantTeams[0]
	}

	if m := got[memberExt]; m.OrgUnitExternalID != unit.Spec.ExternalID || fmt.Sprint(m.TeamExternalIDs) != fmt.Sprint(wantTeams) {
		t.Errorf("member = %+v, want org unit %s and teams %v of this source only", m, unit.Spec.ExternalID, wantTeams)
	}

	if l := got[loneExt]; l.OrgUnitExternalID != "" || l.TeamExternalIDs == nil || len(l.TeamExternalIDs) != 0 {
		t.Errorf("lone user = %+v, want no org unit and an empty team list", l)
	}
}
