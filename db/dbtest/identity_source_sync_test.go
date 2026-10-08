package dbtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/identitysync"
	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests cover the organization sync of identity sources (migration
// 110): the sync fields of spec and status, the trigger that keeps them on a
// write that leaves them out, api.request_identity_source_sync, the sync
// columns of api.external_identities and api.list_identity_source_sync_users,
// and identitysync.Syncer against the real PostgREST and GoTrue.

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

	// The spec of migration 105 only, without the bind password.
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

// goTrueAdmin is the GoTrue admin client neutree-core uses.
func goTrueAdmin(t *testing.T) auth.Client {
	t.Helper()

	return gotrue.New("", "").WithCustomGoTrueURL(GetGoTrueURL()).WithToken(gotrueServiceToken(t))
}

type staticDirectory struct {
	snapshot *orgsync.Snapshot
}

func (d *staticDirectory) Snapshot(context.Context) (*orgsync.Snapshot, error) {
	copied := *d.snapshot
	copied.Users = append([]orgsync.User(nil), d.snapshot.Users...)

	return &copied, nil
}

// cleanupSyncedUsers deletes the GoTrue users linked under linkSource.
func cleanupSyncedUsers(t *testing.T, linkSource string) {
	t.Helper()

	t.Cleanup(func() {
		rows, err := GetTestDB(t).Query(`SELECT user_id FROM api.external_identities WHERE source = $1`, linkSource)
		if err != nil {
			t.Errorf("failed to list synced users: %v", err)
			return
		}
		defer rows.Close()

		var ids []string

		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}

		for _, id := range ids {
			deleteGoTrueUser(t, id)
		}
	})
}

// TestIdentitySourceSync_SyncerEndToEnd runs the sync against the real
// PostgREST and GoTrue: the first sync creates the tree, teams, users that
// never logged in and memberships; the second changes nothing; a disabled
// user is banned (no session, no refresh) and comes back when enabled.
func TestIdentitySourceSync_SyncerEndToEnd(t *testing.T) {
	f := newOrgFixture(t)
	linkSource := auth.LinkSource(auth.LDAPSource, f.source)
	cleanupSyncedUsers(t, linkSource)

	suffix := fmt.Sprint(time.Now().UnixNano())
	id := func(s string) string { return s + "-" + suffix }

	dir := &staticDirectory{snapshot: &orgsync.Snapshot{
		OrgUnits: []orgsync.OrgUnit{
			{ExternalID: id("rnd"), DisplayName: "R&D"},
			{ExternalID: id("ml"), ParentExternalID: id("rnd"), DisplayName: "ML"},
		},
		Teams: []orgsync.Team{{ExternalID: id("core"), DisplayName: "Core"}},
		Users: []orgsync.User{
			{ExternalID: id("lin"), Username: "lin", Email: "lin@example.org", DisplayName: "Lin", OrgUnitExternalID: id("ml"), TeamExternalIDs: []string{id("core")}},
			{ExternalID: id("chen"), Username: "chen", DisplayName: "Chen", OrgUnitExternalID: id("rnd")},
			{ExternalID: id("off"), Username: "off", Disabled: true},
		},
	}}

	syncer := &identitysync.Syncer{Store: f.st, Auth: goTrueAdmin(t)}
	ctx := context.Background()

	result, err := syncer.Run(ctx, f.source, dir)
	if err != nil {
		t.Fatalf("first sync failed: %v (%+v)", err, result)
	}

	if result.Created != 5 || result.Memberships != 3 || result.Pending() != 0 {
		t.Errorf("first sync = %+v, want 5 created and 3 memberships", result)
	}

	users, err := f.st.ListIdentitySourceSyncUsers(f.source, linkSource)
	if err != nil || len(users) != 2 {
		t.Fatalf("ListIdentitySourceSyncUsers = %+v, %v; want lin and chen", users, err)
	}

	byExternalID := map[string]storage.IdentitySourceSyncUser{}
	for _, u := range users {
		byExternalID[u.ExternalID] = u
	}

	lin := byExternalID[id("lin")]
	if lin.Username != "lin" || lin.Email != "lin@example.org" || lin.DisplayName != "Lin" ||
		lin.OrgUnitExternalID != id("ml") || len(lin.TeamExternalIDs) != 1 || lin.TeamExternalIDs[0] != id("core") {
		t.Errorf("lin as stored = %+v", lin)
	}

	profile, err := f.st.GetUserProfile(lin.UserID)
	if err != nil || profile.Spec.Email != "lin@example.org" {
		t.Errorf("lin's profile = %+v, %v; want the directory email", profile, err)
	}

	if chen := byExternalID[id("chen")]; chen.Email != "" || chen.DisplayName != "Chen" || chen.OrgUnitExternalID != id("rnd") {
		t.Errorf("chen as stored = %+v", chen)
	}

	t.Run("second sync writes nothing", func(t *testing.T) {
		result, err := syncer.Run(ctx, f.source, dir)
		if err != nil || result.Planned != 0 {
			t.Errorf("second sync = %+v, %v; want an empty plan", result, err)
		}
	})

	t.Run("a login finds the pre-created user", func(t *testing.T) {
		users := &auth.ExternalUsers{Client: goTrueAdmin(t), Links: f.st}

		userID, created, err := users.Ensure(ctx, auth.LDAPAccount(f.source, id("lin"), "lin", "Lin", "lin@example.org"))
		if err != nil || created || userID != lin.UserID {
			t.Errorf("Ensure = %s, %v, %v; want the synced user %s", userID, created, err, lin.UserID)
		}
	})

	issuer := auth.NewSessionIssuer(GetGoTrueURL(), gotrueServiceToken(t))
	chenID := byExternalID[id("chen")].UserID
	chenEmail := auth.LDAPPlaceholderEmail(f.source, id("chen"))

	link, err := issuer.GenerateMagicLink(ctx, chenEmail)
	if err != nil {
		t.Fatalf("generate_link for chen failed: %v", err)
	}

	raw, err := issuer.VerifyMagicLink(ctx, link.HashedToken)
	if err != nil {
		t.Fatalf("verify for chen failed: %v", err)
	}

	var session struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(raw, &session); err != nil || session.RefreshToken == "" {
		t.Fatalf("no refresh token in %s", raw)
	}

	dir.snapshot.Users[1].Disabled = true

	t.Run("disabled user is banned", func(t *testing.T) {
		result, err := syncer.Run(ctx, f.source, dir)
		if err != nil || result.Deactivated != 1 {
			t.Fatalf("sync = %+v, %v; want one deactivation", result, err)
		}

		user, err := goTrueAdmin(t).AdminGetUser(types.AdminGetUserRequest{UserID: uuid.MustParse(chenID)})
		if err != nil || !auth.IsBanned(&user.User, time.Now()) {
			t.Fatalf("chen = %+v, %v; want banned", user, err)
		}

		if code, body := refreshSession(t, session.RefreshToken); code == http.StatusOK {
			t.Errorf("refresh of a banned user's session = HTTP %d: %s", code, body)
		}

		if link, err := issuer.GenerateMagicLink(ctx, chenEmail); err == nil {
			if _, err := issuer.VerifyMagicLink(ctx, link.HashedToken); err == nil {
				t.Error("a banned user verified a magic link into a session")
			}
		}

		var marker string
		if err := GetTestDB(t).QueryRow(`SELECT sync_deactivated FROM api.external_identities WHERE source = $1 AND external_id = $2`,
			linkSource, id("chen")).Scan(&marker); err != nil || marker != storage.SyncDeactivatedBanned {
			t.Errorf("sync_deactivated = %q, %v; want banned", marker, err)
		}
	})

	dir.snapshot.Users[1].Disabled = false

	t.Run("enabled again is unbanned", func(t *testing.T) {
		result, err := syncer.Run(ctx, f.source, dir)
		if err != nil || result.Reactivated != 1 {
			t.Fatalf("sync = %+v, %v; want one reactivation", result, err)
		}

		user, err := goTrueAdmin(t).AdminGetUser(types.AdminGetUserRequest{UserID: uuid.MustParse(chenID)})
		if err != nil || auth.IsBanned(&user.User, time.Now()) {
			t.Fatalf("chen = %+v, %v; want not banned", user, err)
		}

		link, err := issuer.GenerateMagicLink(ctx, chenEmail)
		if err != nil {
			t.Fatalf("generate_link failed: %v", err)
		}

		if _, err := issuer.VerifyMagicLink(ctx, link.HashedToken); err != nil {
			t.Errorf("verify after reactivation failed: %v", err)
		}
	})

	t.Run("rename keeps the ids", func(t *testing.T) {
		dir.snapshot.OrgUnits[1].DisplayName = "Machine Learning"
		dir.snapshot.Users[0].DisplayName = "Lin Wei"

		result, err := syncer.Run(ctx, f.source, dir)
		if err != nil || result.Updated != 2 {
			t.Fatalf("sync = %+v, %v; want two updates", result, err)
		}

		units, err := f.st.ListOrgUnit(storage.ListOption{Filters: []storage.Filter{{Column: "spec->>external_id", Operator: "eq", Value: id("ml")}}})
		if err != nil || len(units) != 1 || units[0].Metadata.DisplayName != "Machine Learning" ||
			units[0].Metadata.Name != identitysync.ObjectName(f.source, id("ml")) || units[0].Spec.Parent == "" ||
			units[0].Metadata.CreationTimestamp == "" {
			t.Errorf("ml after rename = %+v", units)
		}

		profile, err := f.st.GetUserProfile(lin.UserID)
		if err != nil || profile.Metadata.DisplayName != "Lin Wei" || profile.Metadata.Name == "" {
			t.Errorf("lin's profile after rename = %+v, %v", profile, err)
		}
	})
}

// refreshSession asks GoTrue for a new session with a refresh token.
func refreshSession(t *testing.T, refreshToken string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, GetGoTrueURL()+"/token?grant_type=refresh_token",
		strings.NewReader(mustJSON(map[string]any{"refresh_token": refreshToken})))
	if err != nil {
		t.Fatalf("failed to build the request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	return resp.StatusCode, string(raw)
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
		`{"p_identity_source":"cols","p_link_source":"ldap:cols"}`); code != http.StatusUnauthorized && code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("list_identity_source_sync_users as admin = HTTP %d, want denied: %s", code, raw)
	}
}
