package dbtest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Tests for group role assignments: a Team or an OrgUnit as the subject
// of a role assignment.

// userWithPermissions creates a user holding a global role with exactly the
// given permissions and returns its id and access token.
func userWithPermissions(t *testing.T, prefix string, permissions []string) (string, string) {
	t.Helper()
	waitForPostgREST(t)

	username := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	email := username + "@group-ra.test.local"

	var id string
	if err := execWithContext(t, GetTestDB(t), nil, func(tx *sql.Tx) error {
		id = createUserWithPermissions(t, tx, username, email, permissions)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	return id, gotruePasswordLogin(t, email, "password123").AccessToken
}

func groupAssignmentBody(name, kind, subject, role string) string {
	return mustJSON(map[string]any{
		"api_version": "v1",
		"kind":        "RoleAssignment",
		"metadata":    map[string]any{"name": name},
		"spec":        map[string]any{"global": true, "role": role, "subject_kind": kind, "subject_name": subject},
	})
}

// assignGroup stores a global group assignment directly (no RLS) and removes
// it when the test ends.
func assignGroup(t *testing.T, kind, subject, role string) string {
	t.Helper()

	db := GetTestDB(t)
	name := seedRoleAssignment(t, db, roleAssignment{Global: true, Role: role, SubjectKind: kind, SubjectName: subject})

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (metadata).name = $1`, name)
	})

	return name
}

func hasPermission(t *testing.T, userID, permission, workspace string) bool {
	t.Helper()

	var ok bool
	if err := GetTestDB(t).QueryRow(`SELECT api.has_permission($1::uuid, $2::api.permission_action, NULLIF($3, ''))`,
		userID, permission, workspace).Scan(&ok); err != nil {
		t.Fatalf("has_permission failed: %v", err)
	}

	return ok
}

func TestGroupRoleAssignment_Team(t *testing.T) {
	admin := adminToken(t)
	user := createGoTrueTestUser(t, "gra-team", "password123")

	team := newOrgName("gra-t")
	teamID := createManualTeam(t, admin, team)

	code, raw := postgrestAs(t, admin, http.MethodPost, "/role_assignments", groupAssignmentBody(newOrgName("gra"), "Team", team, "workspace-user"))
	if code != http.StatusCreated {
		t.Fatalf("POST a Team assignment = HTTP %d: %s", code, raw)
	}

	var created []v1.RoleAssignment
	if err := json.Unmarshal([]byte(raw), &created); err != nil || len(created) != 1 {
		t.Fatalf("POST returned %s: %v", raw, err)
	}

	t.Cleanup(func() {
		_, _ = GetTestDB(t).Exec(`DELETE FROM api.role_assignments WHERE id = $1`, created[0].ID)
	})

	if s := created[0].Spec; s.UserID != "" || s.SubjectKind != "Team" || s.SubjectName != team || !s.Global {
		t.Fatalf("stored spec = %+v", s)
	}

	if hasPermission(t, user.ID, "endpoint:read", "default") {
		t.Fatal("the user has endpoint:read before joining the Team")
	}

	body := mustJSON(map[string]any{"team_id": teamID, "user_id": user.ID})
	if code, raw := postgrestAs(t, admin, http.MethodPost, "/team_members", body); code != http.StatusCreated {
		t.Fatalf("POST /team_members = HTTP %d: %s", code, raw)
	}

	if !hasPermission(t, user.ID, "endpoint:read", "default") {
		t.Error("a member of the Team lacks endpoint:read")
	}

	path := fmt.Sprintf("/team_members?team_id=eq.%d&user_id=eq.%s", teamID, user.ID)
	if code, raw := postgrestAs(t, admin, http.MethodDelete, path, ""); code != http.StatusOK {
		t.Fatalf("DELETE /team_members = HTTP %d: %s", code, raw)
	}

	if hasPermission(t, user.ID, "endpoint:read", "default") {
		t.Error("a removed member keeps endpoint:read")
	}

	t.Run("a bound Team cannot be deleted", func(t *testing.T) {
		code, raw := postgrestAs(t, admin, http.MethodDelete, fmt.Sprintf("/teams?id=eq.%d", teamID), "")
		if code != http.StatusConflict || !strings.Contains(raw, "10295") {
			t.Fatalf("DELETE a bound Team = HTTP %d, want 409 10295: %s", code, raw)
		}

		// A soft-deleted assignment is on its way out and blocks nothing.
		if _, err := GetTestDB(t).Exec(`UPDATE api.role_assignments SET metadata.deletion_timestamp = CURRENT_TIMESTAMP WHERE id = $1`, created[0].ID); err != nil {
			t.Fatal(err)
		}

		if code, raw := postgrestAs(t, admin, http.MethodDelete, fmt.Sprintf("/teams?id=eq.%d", teamID), ""); code != http.StatusOK || !strings.Contains(raw, team) {
			t.Fatalf("DELETE a Team bound only by a soft-deleted assignment = HTTP %d: %s", code, raw)
		}
	})
}

func TestGroupRoleAssignment_InactiveTeam(t *testing.T) {
	f := newOrgFixture(t)
	team := f.createTeam()
	user := createGoTrueTestUser(t, "gra-synced", "password123")

	if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: team.ID, UserID: user.ID}); err != nil {
		t.Fatalf("sync adding a member failed: %v", err)
	}

	assignGroup(t, "Team", team.Metadata.Name, "workspace-user")

	if !hasPermission(t, user.ID, "endpoint:read", "default") {
		t.Fatal("a member of an Active synced Team lacks endpoint:read")
	}

	if err := f.st.UpdateTeam(team.GetID(), &v1.Team{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatalf("sync deactivating the Team failed: %v", err)
	}

	if hasPermission(t, user.ID, "endpoint:read", "default") {
		t.Error("a member of an Inactive Team keeps endpoint:read")
	}
}

func TestGroupRoleAssignment_OrgUnitSubtree(t *testing.T) {
	f := newOrgFixture(t)
	parent := f.createOrgUnit("")
	child := f.createOrgUnit(parent.Metadata.Name)
	other := f.createOrgUnit("")

	inChild := createGoTrueTestUser(t, "gra-child", "password123")
	inOther := createGoTrueTestUser(t, "gra-other", "password123")

	for user, unit := range map[string]int{inChild.ID: child.ID, inOther.ID: other.ID} {
		if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: user, OrgUnitID: unit, IdentitySource: f.source}); err != nil {
			t.Fatalf("SetOrgUnitMember failed: %v", err)
		}
	}

	assignGroup(t, "OrgUnit", parent.Metadata.Name, "workspace-user")

	if !hasPermission(t, inChild.ID, "endpoint:read", "default") {
		t.Error("a member of a child OrgUnit lacks the parent's assignment")
	}

	if hasPermission(t, inOther.ID, "endpoint:read", "default") {
		t.Error("a member outside the subtree got the assignment")
	}

	if err := f.st.UpdateOrgUnit(parent.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatalf("sync deactivating the parent failed: %v", err)
	}

	if hasPermission(t, inChild.ID, "endpoint:read", "default") {
		t.Error("the assignment of an Inactive OrgUnit still grants")
	}

	if err := f.st.UpdateOrgUnit(parent.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseActive}}); err != nil {
		t.Fatal(err)
	}

	if err := f.st.UpdateOrgUnit(child.GetID(), &v1.OrgUnit{Status: &v1.OrgStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatal(err)
	}

	if hasPermission(t, inChild.ID, "endpoint:read", "default") {
		t.Error("a member of an Inactive OrgUnit still gets the parent's assignment")
	}
}

func TestGroupRoleAssignment_JoiningIsAGrant(t *testing.T) {
	admin := adminToken(t)

	assignerID, assigner := userWithPermissions(t, "gra-assigner", []string{"team:read", "team:assign-member"})

	bound := newOrgName("gra-admins")
	boundID := createManualTeam(t, admin, bound)
	assignGroup(t, "Team", bound, "admin")

	plain := newOrgName("gra-plain")
	plainID := createManualTeam(t, admin, plain)

	body := mustJSON(map[string]any{"team_id": boundID, "user_id": assignerID})
	if code, raw := postgrestAs(t, assigner, http.MethodPost, "/team_members", body); code != http.StatusForbidden || !strings.Contains(raw, "10296") {
		t.Fatalf("joining a Team bound to admin = HTTP %d, want 403 10296: %s", code, raw)
	}

	if hasPermission(t, assignerID, "role_assignment:create", "") {
		t.Fatal("the assigner became an admin")
	}

	body = mustJSON(map[string]any{"team_id": plainID, "user_id": assignerID})
	if code, raw := postgrestAs(t, assigner, http.MethodPost, "/team_members", body); code != http.StatusCreated {
		t.Errorf("joining a Team with no assignment = HTTP %d: %s", code, raw)
	}

	// Someone who could create the assignment may add the member.
	member := createGoTrueTestUser(t, "gra-member", "password123")
	body = mustJSON(map[string]any{"team_id": boundID, "user_id": member.ID})
	if code, raw := postgrestAs(t, admin, http.MethodPost, "/team_members", body); code != http.StatusCreated {
		t.Errorf("admin adding a member to a bound Team = HTTP %d: %s", code, raw)
	}

	t.Run("sync is not checked", func(t *testing.T) {
		f := newOrgFixture(t)
		synced := f.createTeam()
		assignGroup(t, "Team", synced.Metadata.Name, "admin")

		if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: synced.ID, UserID: member.ID}); err != nil {
			t.Errorf("sync adding a member to a bound Team failed: %v", err)
		}
	})

	t.Run("OrgUnit memberships", func(t *testing.T) {
		f := newOrgFixture(t)
		root := f.createOrgUnit("")
		left := f.createOrgUnit(root.Metadata.Name)
		right := f.createOrgUnit(root.Metadata.Name)
		assignGroup(t, "OrgUnit", left.Metadata.Name, "admin")

		_, unitAssigner := userWithPermissions(t, "gra-ou-assigner", []string{"org_unit:read", "org_unit:assign-member"})
		local := createGoTrueTestUser(t, "gra-ou-local", "password123")

		insert := func(unit int) (int, string) {
			return postgrestAs(t, unitAssigner, http.MethodPost, "/org_unit_members", mustJSON(map[string]any{"user_id": local.ID, "org_unit_id": unit}))
		}

		if code, raw := insert(left.ID); code != http.StatusForbidden || !strings.Contains(raw, "10296") {
			t.Fatalf("putting a user into a bound OrgUnit = HTTP %d, want 403 10296: %s", code, raw)
		}

		if code, raw := insert(right.ID); code != http.StatusCreated {
			t.Fatalf("putting a user into an unbound OrgUnit = HTTP %d: %s", code, raw)
		}

		t.Cleanup(func() {
			_, _ = GetTestDB(t).Exec(`DELETE FROM api.org_unit_members WHERE user_id = $1`, local.ID)
		})

		patch := mustJSON(map[string]any{"org_unit_id": left.ID})
		if code, raw := postgrestAs(t, unitAssigner, http.MethodPatch, "/org_unit_members?user_id=eq."+local.ID, patch); code != http.StatusForbidden || !strings.Contains(raw, "10296") {
			t.Errorf("moving a user into a bound OrgUnit = HTTP %d, want 403 10296: %s", code, raw)
		}

		// An assignment on an OrgUnit the user is already under is not a new grant.
		assignGroup(t, "OrgUnit", root.Metadata.Name, "admin")

		child := f.createOrgUnit(right.Metadata.Name)
		patch = mustJSON(map[string]any{"org_unit_id": child.ID})
		if code, raw := postgrestAs(t, unitAssigner, http.MethodPatch, "/org_unit_members?user_id=eq."+local.ID, patch); code != http.StatusOK || !strings.Contains(raw, local.ID) {
			t.Errorf("moving a user below an OrgUnit it is already under = HTTP %d: %s", code, raw)
		}
	})
}

func TestGroupRoleAssignment_Validation(t *testing.T) {
	admin := adminToken(t)
	team := newOrgName("gra-v")
	createManualTeam(t, admin, team)

	f := newOrgFixture(t)
	unit := f.createOrgUnit("")
	user := createGoTrueTestUser(t, "gra-v-user", "password123")

	post := func(spec map[string]any) (int, string) {
		name := newOrgName("gra")
		t.Cleanup(func() {
			_, _ = GetTestDB(t).Exec(`DELETE FROM api.role_assignments WHERE (metadata).name = $1`, name)
		})

		return postgrestAs(t, admin, http.MethodPost, "/role_assignments", mustJSON(map[string]any{
			"api_version": "v1", "kind": "RoleAssignment",
			"metadata": map[string]any{"name": name},
			"spec":     spec,
		}))
	}

	bad := []struct {
		what string
		spec map[string]any
		code string
	}{
		{"unknown kind", map[string]any{"global": true, "role": "admin", "subject_kind": "Group", "subject_name": team}, "10292"},
		{"Team with a user", map[string]any{"global": true, "role": "admin", "subject_kind": "Team", "subject_name": team, "user_id": user.ID}, "10293"},
		{"Team without a name", map[string]any{"global": true, "role": "admin", "subject_kind": "Team"}, "10293"},
		{"User with a group name", map[string]any{"global": true, "role": "admin", "user_id": user.ID, "subject_name": team}, "10293"},
		{"missing Team", map[string]any{"global": true, "role": "admin", "subject_kind": "Team", "subject_name": "no-such-team"}, "10294"},
		{"OrgUnit named as a Team", map[string]any{"global": true, "role": "admin", "subject_kind": "Team", "subject_name": unit.Metadata.Name}, "10294"},
		{"missing OrgUnit", map[string]any{"global": true, "role": "admin", "subject_kind": "OrgUnit", "subject_name": "no-such-unit"}, "10294"},
		{"no subject", map[string]any{"global": true, "role": "admin"}, "10032"},
	}

	for _, c := range bad {
		if code, raw := post(c.spec); code != http.StatusBadRequest || !strings.Contains(raw, c.code) {
			t.Errorf("%s = HTTP %d, want 400 %s: %s", c.what, code, c.code, raw)
		}
	}

	if code, raw := post(map[string]any{"global": true, "role": "workspace-user", "subject_kind": "OrgUnit", "subject_name": unit.Metadata.Name}); code != http.StatusCreated {
		t.Errorf("an OrgUnit assignment = HTTP %d: %s", code, raw)
	}

	code, raw := post(map[string]any{"global": true, "role": "workspace-user", "subject_kind": "Team", "subject_name": team})
	if code != http.StatusCreated {
		t.Fatalf("a Team assignment = HTTP %d: %s", code, raw)
	}

	if code, raw := post(map[string]any{"global": true, "role": "workspace-user", "subject_kind": "Team", "subject_name": team}); code != http.StatusConflict {
		t.Errorf("the same Team assignment again = HTTP %d, want 409: %s", code, raw)
	}

	var rows []v1.RoleAssignment
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("POST returned %s: %v", raw, err)
	}

	id := rows[0].ID
	stored := func() v1.RoleAssignmentSpec {
		t.Helper()

		code, raw := postgrestAs(t, admin, http.MethodGet, fmt.Sprintf("/role_assignments?id=eq.%d", id), "")
		var got []v1.RoleAssignment
		if code != http.StatusOK || json.Unmarshal([]byte(raw), &got) != nil || len(got) != 1 {
			t.Fatalf("GET = HTTP %d: %s", code, raw)
		}

		return *got[0].Spec
	}

	t.Run("a PATCH without the subject keeps it", func(t *testing.T) {
		patch := mustJSON(map[string]any{"spec": map[string]any{"global": true, "role": "admin"}})
		if code, raw := postgrestAs(t, admin, http.MethodPatch, fmt.Sprintf("/role_assignments?id=eq.%d", id), patch); code != http.StatusOK {
			t.Fatalf("PATCH spec.role = HTTP %d: %s", code, raw)
		}

		if s := stored(); s.SubjectKind != "Team" || s.SubjectName != team || s.UserID != "" || s.Role != "admin" {
			t.Errorf("spec after a partial PATCH = %+v", s)
		}

		patch = mustJSON(map[string]any{"status": map[string]any{"phase": "Created"}})
		if code, raw := postgrestAs(t, admin, http.MethodPatch, fmt.Sprintf("/role_assignments?id=eq.%d", id), patch); code != http.StatusOK {
			t.Fatalf("PATCH status = HTTP %d: %s", code, raw)
		}

		if s := stored(); s.SubjectKind != "Team" || s.SubjectName != team {
			t.Errorf("spec after a status PATCH = %+v", s)
		}

		// Through the Go storage, as a controller writes.
		st := NewTestStorage(t)
		if err := st.UpdateRoleAssignment(fmt.Sprint(id), &v1.RoleAssignment{Spec: &v1.RoleAssignmentSpec{Global: true, Role: "workspace-user"}}); err != nil {
			t.Fatalf("UpdateRoleAssignment failed: %v", err)
		}

		if s := stored(); s.SubjectKind != "Team" || s.SubjectName != team || s.Role != "workspace-user" {
			t.Errorf("spec after a storage update = %+v", s)
		}
	})

	t.Run("a PATCH with only the role keeps the scope", func(t *testing.T) {
		patch := mustJSON(map[string]any{"spec": map[string]any{"role": "admin"}})
		if code, raw := postgrestAs(t, admin, http.MethodPatch, fmt.Sprintf("/role_assignments?id=eq.%d", id), patch); code != http.StatusOK {
			t.Fatalf("PATCH spec.role = HTTP %d: %s", code, raw)
		}

		if s := stored(); !s.Global || s.SubjectName != team || s.Role != "admin" {
			t.Errorf("spec after a role-only PATCH = %+v", s)
		}
	})

	// Created here so its cleanup runs after the assignments' cleanups.
	other := newOrgName("gra-sd")
	createManualTeam(t, admin, other)

	t.Run("a soft-deleted assignment can be created again at once", func(t *testing.T) {
		spec := map[string]any{"global": true, "role": "workspace-user", "subject_kind": "Team", "subject_name": other}
		if code, raw := post(spec); code != http.StatusCreated {
			t.Fatalf("a Team assignment = HTTP %d: %s", code, raw)
		}

		if _, err := GetTestDB(t).Exec(`UPDATE api.role_assignments SET metadata.deletion_timestamp = CURRENT_TIMESTAMP
			WHERE (spec).subject_name = $1`, other); err != nil {
			t.Fatal(err)
		}

		if code, raw := post(spec); code != http.StatusCreated {
			t.Errorf("the same Team assignment after a soft delete = HTTP %d, want 201: %s", code, raw)
		}
	})

	t.Run("user assignments read as User", func(t *testing.T) {
		var kinds []string
		rows, err := GetTestDB(t).Query(`SELECT DISTINCT COALESCE((spec).subject_kind, '<null>') FROM api.role_assignments WHERE (spec).user_id IS NOT NULL`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				t.Fatal(err)
			}

			kinds = append(kinds, k)
		}

		if len(kinds) != 1 || kinds[0] != "User" {
			t.Errorf("subject kinds of user assignments = %v, want [User]", kinds)
		}

		patch := mustJSON(map[string]any{"spec": map[string]any{"global": true, "role": "workspace-user"}})
		name := assignUser(t, user.ID, "admin")
		if code, raw := postgrestAs(t, admin, http.MethodPatch, "/role_assignments?metadata->>name=eq."+name, patch); code != http.StatusOK || !strings.Contains(raw, user.ID) {
			t.Errorf("PATCH a user assignment without user_id = HTTP %d: %s", code, raw)
		}
	})

	t.Run("only global readers see Team assignments", func(t *testing.T) {
		reader := userTokenWithPermissions(t, "gra-reader", []string{"role_assignment:read"})

		path := fmt.Sprintf("/role_assignments?id=eq.%d", id)
		if code, raw := postgrestAs(t, reader, http.MethodGet, path, ""); code != http.StatusOK || !strings.Contains(raw, team) {
			t.Errorf("global reader: GET = HTTP %d: %s", code, raw)
		}

		none := userTokenWithPermissions(t, "gra-none", nil)
		if code, raw := postgrestAs(t, none, http.MethodGet, path, ""); code != http.StatusOK || raw != "[]" {
			t.Errorf("user without role_assignment:read: GET = HTTP %d: %s", code, raw)
		}
	})
}

func assignUser(t *testing.T, userID, role string) string {
	t.Helper()

	db := GetTestDB(t)
	name := seedRoleAssignment(t, db, roleAssignment{UserID: userID, Global: true, Role: role})

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (metadata).name = $1`, name)
	})

	return name
}

// TestGroupRoleAssignment_Performance times api.has_permission and a role
// assignment list with 1000 group assignments. Set GROUP_RA_PERF=1 to run.
func TestGroupRoleAssignment_Performance(t *testing.T) {
	if os.Getenv("GROUP_RA_PERF") == "" {
		t.Skip("set GROUP_RA_PERF=1 to run")
	}

	db := GetTestDB(t)
	f := newOrgFixture(t)
	prefix := newOrgName("perf")

	// An OrgUnit chain of depth 5 with a member at the bottom.
	parent := ""
	var leaf *v1.OrgUnit
	for i := 0; i < 5; i++ {
		leaf = f.createOrgUnit(parent)
		parent = leaf.Metadata.Name
	}

	user := createGoTrueTestUser(t, "gra-perf", "password123")
	if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: user.ID, OrgUnitID: leaf.ID, IdentitySource: f.source}); err != nil {
		t.Fatal(err)
	}

	// 1000 manual Teams, each with one assignment and 20 members drawn from 50
	// users; the measured user is in 200 of them. The OrgUnit chain gets an
	// assignment per node.
	if _, err := db.Exec(`
		INSERT INTO api.teams (api_version, kind, metadata, spec, status)
		SELECT 'v1', 'Team',
			ROW($1 || '-' || g, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, NULL)::api.team_spec, NULL
		FROM generate_series(1, 1000) g`, prefix); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (metadata).name LIKE $1 || '-%'`, prefix)
		_, _ = db.Exec(`DELETE FROM api.teams WHERE (metadata).name LIKE $1 || '-%'`, prefix)
	})

	var others []string
	for i := 0; i < 50; i++ {
		others = append(others, createGoTrueTestUser(t, fmt.Sprintf("gra-perf-%d", i), "password123").ID)
	}

	if _, err := db.Exec(`
		INSERT INTO api.team_members (team_id, user_id)
		SELECT t.id, u.id
		FROM api.teams t
		CROSS JOIN LATERAL (
			SELECT x::uuid AS id FROM unnest($2::text[]) WITH ORDINALITY AS o(x, n)
			WHERE (o.n + t.id) % 50 < 20
		) u
		WHERE (t.metadata).name LIKE $1 || '-%'`, prefix, "{"+strings.Join(others, ",")+"}"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`
		INSERT INTO api.team_members (team_id, user_id)
		SELECT t.id, $2::uuid FROM api.teams t
		WHERE (t.metadata).name LIKE $1 || '-%' AND t.id % 5 = 0`, prefix, user.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		SELECT 'v1', 'RoleAssignment',
			ROW((t.metadata).name, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, NULL, TRUE, 'workspace-user', NULL, 'Team', (t.metadata).name, NULL)::api.role_assignment_spec
		FROM api.teams t WHERE (t.metadata).name LIKE $1 || '-%'`, prefix); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		SELECT 'v1', 'RoleAssignment',
			ROW($1 || '-ou-' || u.id, NULL, NULL, NULL, NULL, NULL, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, NULL, TRUE, 'workspace-user', NULL, 'OrgUnit', (u.metadata).name, NULL)::api.role_assignment_spec
		FROM api.org_units u WHERE (u.spec).identity_source = $2`, prefix, f.source); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`ANALYZE api.role_assignments; ANALYZE api.team_members; ANALYZE api.teams; ANALYZE api.org_units`); err != nil {
		t.Fatal(err)
	}

	var total int
	_ = db.QueryRow(`SELECT count(*) FROM api.role_assignments`).Scan(&total)

	// One round trip per measurement: has_permission is volatile, so it runs
	// once per generated row.
	timeIt := func(what, userID, permission, workspace string) {
		const n = 200

		start := time.Now()

		var granted int
		if err := db.QueryRow(`SELECT count(*) FILTER (WHERE api.has_permission($1::uuid, $2::api.permission_action, NULLIF($3, '')))
			FROM generate_series(1, $4::int)`, userID, permission, workspace, n).Scan(&granted); err != nil {
			t.Fatalf("%s failed: %v", what, err)
		}

		t.Logf("%s: %.3f ms per call (%d calls, granted %d)", what, float64(time.Since(start).Microseconds())/1000/n, n, granted)
	}

	adminID := ""
	_ = db.QueryRow(`SELECT id::text FROM api.user_profiles WHERE (metadata).name = 'admin'`).Scan(&adminID)

	few := createGoTrueTestUser(t, "gra-perf-few", "password123")
	if _, err := db.Exec(`
		INSERT INTO api.team_members (team_id, user_id)
		SELECT t.id, $2::uuid FROM api.teams t WHERE (t.metadata).name IN ($1 || '-1', $1 || '-2')`, prefix, few.ID); err != nil {
		t.Fatal(err)
	}

	timeIt("has_permission, own assignment only (admin)", adminID, "endpoint:read", "default")
	timeIt("has_permission, 2 Teams, granted", few.ID, "endpoint:read", "default")
	timeIt("has_permission, 2 Teams, not held", few.ID, "role:create", "")
	timeIt("has_permission, 200 Teams + 5 OrgUnits, granted", user.ID, "endpoint:read", "default")
	timeIt("has_permission, 200 Teams + 5 OrgUnits, not held", user.ID, "role:create", "")
	timeIt("has_permission, 400 Teams, granted", others[0], "endpoint:read", "default")

	admin := adminToken(t)
	for _, who := range []struct{ name, token string }{
		{"admin", admin},
		{"global role_assignment:read", userTokenWithPermissions(t, "gra-perf-reader", []string{"role_assignment:read"})},
	} {
		start := time.Now()
		code, raw := postgrestAs(t, who.token, http.MethodGet, "/role_assignments?select=id", "")
		elapsed := time.Since(start)

		var rows []map[string]any
		if code != http.StatusOK || json.Unmarshal([]byte(raw), &rows) != nil {
			t.Fatalf("list = HTTP %d", code)
		}

		t.Logf("GET /role_assignments as %s: %d of %d rows in %v", who.name, len(rows), total, elapsed)
	}
}

func TestGroupRoleAssignment_EffectiveRPC(t *testing.T) {
	admin := adminToken(t)
	f := newOrgFixture(t)
	parent := f.createOrgUnit("")
	child := f.createOrgUnit(parent.Metadata.Name)

	user := createGoTrueTestUser(t, "gra-eff", "password123")
	if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: user.ID, OrgUnitID: child.ID, IdentitySource: f.source}); err != nil {
		t.Fatal(err)
	}

	team := newOrgName("gra-eff")
	teamID := createManualTeam(t, admin, team)
	body := mustJSON(map[string]any{"team_id": teamID, "user_id": user.ID})
	if code, raw := postgrestAs(t, admin, http.MethodPost, "/team_members", body); code != http.StatusCreated {
		t.Fatalf("POST /team_members = HTTP %d: %s", code, raw)
	}

	own := assignUser(t, user.ID, "workspace-user")
	viaTeam := assignGroup(t, "Team", team, "workspace-user")
	viaParent := assignGroup(t, "OrgUnit", parent.Metadata.Name, "workspace-user")

	// Not effective: a soft-deleted assignment and one of another Team.
	gone := assignGroup(t, "Team", team, "department-admin")
	if _, err := GetTestDB(t).Exec(`UPDATE api.role_assignments SET metadata.deletion_timestamp = CURRENT_TIMESTAMP WHERE (metadata).name = $1`, gone); err != nil {
		t.Fatal(err)
	}

	other := newOrgName("gra-eff-o")
	createManualTeam(t, admin, other)
	assignGroup(t, "Team", other, "admin")

	rpc := func(token string) (int, string) {
		return postgrestAs(t, token, http.MethodPost, "/rpc/get_user_effective_role_assignments", mustJSON(map[string]any{"p_user_id": user.ID}))
	}

	code, raw := rpc(admin)
	if code != http.StatusOK {
		t.Fatalf("RPC as admin = HTTP %d: %s", code, raw)
	}

	var rows []struct {
		ID      int                   `json:"id"`
		Name    string                `json:"name"`
		Spec    v1.RoleAssignmentSpec `json:"spec"`
		ViaKind string                `json:"via_kind"`
		ViaName *string               `json:"via_name"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}

	got := map[string]string{}
	for _, r := range rows {
		via := ""
		if r.ViaName != nil {
			via = *r.ViaName
		}

		got[r.Name] = r.ViaKind + "/" + via
	}

	want := map[string]string{
		own:       "User/",
		viaTeam:   "Team/" + team,
		viaParent: "OrgUnit/" + parent.Metadata.Name,
	}
	if len(got) != len(want) {
		t.Errorf("effective assignments = %v, want %v", got, want)
	}

	for name, via := range want {
		if got[name] != via {
			t.Errorf("%s via %q, want %q", name, got[name], via)
		}
	}

	t.Run("needs role_assignment:read on the user", func(t *testing.T) {
		reader := userTokenWithPermissions(t, "gra-eff-reader", []string{"role_assignment:read"})
		if code, raw := rpc(reader); code != http.StatusOK || !strings.Contains(raw, viaTeam) {
			t.Errorf("global reader = HTTP %d: %s", code, raw)
		}

		for who, token := range map[string]string{
			"no permission": userTokenWithPermissions(t, "gra-eff-none", nil),
			"the user":      gotruePasswordLogin(t, user.Email, "password123").AccessToken,
		} {
			if code, raw := rpc(token); code != http.StatusForbidden || strings.Contains(raw, viaTeam) {
				t.Errorf("%s = HTTP %d, want 403: %s", who, code, raw)
			}
		}
	})
}
