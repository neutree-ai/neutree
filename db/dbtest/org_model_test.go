package dbtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests cover the organization model (migrations 108/109): OrgUnit and
// Team resources, their member tables, the trigger-maintained materialized
// path, the subtree functions and RLS. Sync writes as service_role, which is
// what NewTestStorage uses; users go through PostgREST with their own tokens.

var orgNameSeq atomic.Int64

func newOrgName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano()%1_000_000_000_000, orgNameSeq.Add(1))
}

// orgFixture is one identity source with the OrgUnits and Teams a test syncs
// from it. Everything is removed when the test ends.
type orgFixture struct {
	t      *testing.T
	st     storage.Storage
	source string
}

func newOrgFixture(t *testing.T) *orgFixture {
	t.Helper()

	source := newIdentitySourceName("org")
	createIdentitySource(t, adminToken(t), ldapSourceJSON(source, "", "secret", false))

	f := &orgFixture{t: t, st: NewTestStorage(t), source: source}

	// Registered after the identity source, so it runs before the source is
	// removed. Children go first: a node with children cannot be deleted.
	t.Cleanup(func() {
		db := GetTestDB(t)
		if _, err := db.Exec(`
			DO $$
			DECLARE r RECORD;
			BEGIN
				FOR r IN SELECT id FROM api.org_units WHERE (spec).identity_source = '` + source + `' ORDER BY length(path) DESC LOOP
					DELETE FROM api.org_units WHERE id = r.id;
				END LOOP;
			END $$`); err != nil {
			t.Errorf("failed to delete org units of %s: %v", source, err)
		}

		if _, err := db.Exec(`DELETE FROM api.teams WHERE (spec).identity_source = $1`, source); err != nil {
			t.Errorf("failed to delete teams of %s: %v", source, err)
		}
	})

	return f
}

func (f *orgFixture) orgUnit(name, externalID, parent string) *v1.OrgUnit {
	return &v1.OrgUnit{
		APIVersion: "v1",
		Kind:       "OrgUnit",
		Metadata:   &v1.Metadata{Name: name, DisplayName: "Dept " + externalID},
		Spec:       &v1.OrgUnitSpec{IdentitySource: f.source, ExternalID: externalID, Parent: parent},
	}
}

// createOrgUnit syncs a new OrgUnit under parent (a name, empty for a root)
// and returns it as stored.
func (f *orgFixture) createOrgUnit(parent string) *v1.OrgUnit {
	f.t.Helper()

	name := newOrgName("ou")
	if err := f.st.CreateOrgUnit(f.orgUnit(name, "ext-"+name, parent)); err != nil {
		f.t.Fatalf("CreateOrgUnit(%s) failed: %v", name, err)
	}

	return f.getOrgUnit(name)
}

func (f *orgFixture) getOrgUnit(name string) *v1.OrgUnit {
	f.t.Helper()

	units, err := f.st.ListOrgUnit(storage.ListOption{Filters: []storage.Filter{{Column: "metadata->>name", Operator: "eq", Value: name}}})
	if err != nil || len(units) != 1 {
		f.t.Fatalf("ListOrgUnit(%s) = %d units, %v; want 1", name, len(units), err)
	}

	return &units[0]
}

// moveOrgUnit sets a new parent, sending the whole spec as sync does.
func (f *orgFixture) moveOrgUnit(unit *v1.OrgUnit, parent string) error {
	spec := *unit.Spec
	spec.Parent = parent

	return f.st.UpdateOrgUnit(unit.GetID(), &v1.OrgUnit{Spec: &spec})
}

func (f *orgFixture) createTeam() *v1.Team {
	f.t.Helper()

	name := newOrgName("team")
	err := f.st.CreateTeam(&v1.Team{
		APIVersion: "v1",
		Kind:       "Team",
		Metadata:   &v1.Metadata{Name: name},
		Spec:       &v1.TeamSpec{IdentitySource: f.source, ExternalID: "ext-" + name},
	})
	if err != nil {
		f.t.Fatalf("CreateTeam(%s) failed: %v", name, err)
	}

	teams, err := f.st.ListTeam(storage.ListOption{Filters: []storage.Filter{{Column: "metadata->>name", Operator: "eq", Value: name}}})
	if err != nil || len(teams) != 1 {
		f.t.Fatalf("ListTeam(%s) = %d teams, %v; want 1", name, len(teams), err)
	}

	return &teams[0]
}

func pathOf(ids ...int) string {
	var b strings.Builder
	for _, id := range ids {
		fmt.Fprintf(&b, "/%d", id)
	}

	return b.String() + "/"
}

func assertErrCode(t *testing.T, what string, err error, code string) {
	t.Helper()

	if err == nil {
		t.Fatalf("%s succeeded, want error %s", what, code)
	}

	if !strings.Contains(err.Error(), code) {
		t.Fatalf("%s failed with %v, want error %s", what, err, code)
	}
}

func subtreeIDs(t *testing.T, st storage.Storage, rootID int) []int {
	t.Helper()

	units, err := st.ListOrgUnitSubtree(rootID)
	if err != nil {
		t.Fatalf("ListOrgUnitSubtree(%d) failed: %v", rootID, err)
	}

	ids := make([]int, 0, len(units))
	for _, u := range units {
		ids = append(ids, u.ID)
	}

	sort.Ints(ids)

	return ids
}

func sortedInts(ids ...int) []int {
	sort.Ints(ids)
	return ids
}

func TestOrgUnit_TreeAndMove(t *testing.T) {
	f := newOrgFixture(t)

	a := f.createOrgUnit("")
	b := f.createOrgUnit(a.Metadata.Name)
	c := f.createOrgUnit(b.Metadata.Name)
	d := f.createOrgUnit(c.Metadata.Name)
	e := f.createOrgUnit("")

	t.Run("paths are built from ids", func(t *testing.T) {
		want := map[*v1.OrgUnit]string{
			a: pathOf(a.ID),
			b: pathOf(a.ID, b.ID),
			c: pathOf(a.ID, b.ID, c.ID),
			d: pathOf(a.ID, b.ID, c.ID, d.ID),
			e: pathOf(e.ID),
		}
		for u, path := range want {
			if u.Path != path {
				t.Errorf("%s path = %q, want %q", u.Metadata.Name, u.Path, path)
			}
		}

		if a.Status == nil || a.Status.Phase != v1.OrgPhaseActive {
			t.Errorf("new OrgUnit status = %+v, want phase Active", a.Status)
		}
	})

	t.Run("subtree query", func(t *testing.T) {
		if got, want := subtreeIDs(t, f.st, a.ID), sortedInts(a.ID, b.ID, c.ID, d.ID); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("subtree(a) = %v, want %v", got, want)
		}

		if got, want := subtreeIDs(t, f.st, c.ID), sortedInts(c.ID, d.ID); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("subtree(c) = %v, want %v", got, want)
		}
	})

	t.Run("moving a node re-paths its subtree", func(t *testing.T) {
		if err := f.moveOrgUnit(b, e.Metadata.Name); err != nil {
			t.Fatalf("move b under e failed: %v", err)
		}

		want := map[string]string{
			a.Metadata.Name: pathOf(a.ID),
			b.Metadata.Name: pathOf(e.ID, b.ID),
			c.Metadata.Name: pathOf(e.ID, b.ID, c.ID),
			d.Metadata.Name: pathOf(e.ID, b.ID, c.ID, d.ID),
			e.Metadata.Name: pathOf(e.ID),
		}
		for name, path := range want {
			if got := f.getOrgUnit(name).Path; got != path {
				t.Errorf("%s path = %q, want %q", name, got, path)
			}
		}

		if got, want := subtreeIDs(t, f.st, a.ID), []int{a.ID}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("subtree(a) after the move = %v, want %v", got, want)
		}

		if got, want := subtreeIDs(t, f.st, e.ID), sortedInts(e.ID, b.ID, c.ID, d.ID); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("subtree(e) after the move = %v, want %v", got, want)
		}
	})

	t.Run("an empty parent makes a root", func(t *testing.T) {
		if err := f.moveOrgUnit(f.getOrgUnit(c.Metadata.Name), ""); err != nil {
			t.Fatalf("move c to the root failed: %v", err)
		}

		if got := f.getOrgUnit(d.Metadata.Name).Path; got != pathOf(c.ID, d.ID) {
			t.Errorf("d path = %q, want %q", got, pathOf(c.ID, d.ID))
		}
	})

	t.Run("a written path is ignored", func(t *testing.T) {
		stale := f.getOrgUnit(d.Metadata.Name)
		stale.Path = "/999999/"

		if err := f.st.UpdateOrgUnit(stale.GetID(), stale); err != nil {
			t.Fatalf("UpdateOrgUnit failed: %v", err)
		}

		if got := f.getOrgUnit(d.Metadata.Name).Path; got != pathOf(c.ID, d.ID) {
			t.Errorf("d path = %q, want %q", got, pathOf(c.ID, d.ID))
		}
	})
}

func TestOrgUnit_ParentChecks(t *testing.T) {
	f := newOrgFixture(t)
	other := newOrgFixture(t)

	a := f.createOrgUnit("")
	b := f.createOrgUnit(a.Metadata.Name)
	c := f.createOrgUnit(b.Metadata.Name)
	foreign := other.createOrgUnit("")

	assertErrCode(t, "moving a under its grandchild", f.moveOrgUnit(a, c.Metadata.Name), "10276")
	assertErrCode(t, "moving a under itself", f.moveOrgUnit(a, a.Metadata.Name), "10276")
	assertErrCode(t, "moving b under a missing parent", f.moveOrgUnit(b, "no-such-org-unit"), "10275")
	assertErrCode(t, "moving b under another source's node", f.moveOrgUnit(b, foreign.Metadata.Name), "10275")

	name := newOrgName("ou")
	assertErrCode(t, "creating under a missing parent",
		f.st.CreateOrgUnit(f.orgUnit(name, "ext-"+name, "no-such-org-unit")), "10275")

	// Nothing moved.
	if got := f.getOrgUnit(c.Metadata.Name).Path; got != pathOf(a.ID, b.ID, c.ID) {
		t.Errorf("c path = %q, want %q", got, pathOf(a.ID, b.ID, c.ID))
	}

	t.Run("a node with children cannot be deleted", func(t *testing.T) {
		_, err := GetTestDB(t).Exec(`DELETE FROM api.org_units WHERE id = $1`, b.ID)
		assertErrCode(t, "deleting b", err, "10275")
	})
}

func TestOrgUnit_StableIdentity(t *testing.T) {
	f := newOrgFixture(t)
	a := f.createOrgUnit("")

	t.Run("identity source and external id are unique", func(t *testing.T) {
		name := newOrgName("ou")
		err := f.st.CreateOrgUnit(f.orgUnit(name, a.Spec.ExternalID, ""))
		assertErrCode(t, "creating a second OrgUnit for the same external id", err, "23505")
	})

	t.Run("a rename keeps id, name and path", func(t *testing.T) {
		renamed := *a.Metadata
		renamed.DisplayName = "Renamed department"

		if err := f.st.UpdateOrgUnit(a.GetID(), &v1.OrgUnit{Metadata: &renamed}); err != nil {
			t.Fatalf("rename failed: %v", err)
		}

		got := f.getOrgUnit(a.Metadata.Name)
		if got.ID != a.ID || got.Path != a.Path || got.Metadata.DisplayName != "Renamed department" {
			t.Errorf("after rename: id %d path %q display %q; want id %d path %q display %q",
				got.ID, got.Path, got.Metadata.DisplayName, a.ID, a.Path, "Renamed department")
		}
	})

	t.Run("name and directory identity cannot change", func(t *testing.T) {
		spec := *a.Spec
		spec.ExternalID = "another-id"
		assertErrCode(t, "changing external_id", f.st.UpdateOrgUnit(a.GetID(), &v1.OrgUnit{Spec: &spec}), "10273")

		meta := *a.Metadata
		meta.Name = newOrgName("ou")
		assertErrCode(t, "changing metadata.name", f.st.UpdateOrgUnit(a.GetID(), &v1.OrgUnit{Metadata: &meta}), "10273")
	})

	t.Run("identity source must exist", func(t *testing.T) {
		name := newOrgName("ou")
		unit := f.orgUnit(name, "ext-"+name, "")

		unit.Spec.IdentitySource = "ldap:" + f.source
		assertErrCode(t, "creating with a link-style source", f.st.CreateOrgUnit(unit), "10272")

		unit.Spec.IdentitySource = ""
		assertErrCode(t, "creating without a source", f.st.CreateOrgUnit(unit), "10271")
	})

	t.Run("OrgUnits are global", func(t *testing.T) {
		name := newOrgName("ou")
		unit := f.orgUnit(name, "ext-"+name, "")
		unit.Metadata.Workspace = "default"
		assertErrCode(t, "creating in a workspace", f.st.CreateOrgUnit(unit), "10270")
	})

	t.Run("Teams have the same identity rules", func(t *testing.T) {
		team := f.createTeam()

		dup := &v1.Team{
			APIVersion: "v1", Kind: "Team",
			Metadata: &v1.Metadata{Name: newOrgName("team")},
			Spec:     &v1.TeamSpec{IdentitySource: f.source, ExternalID: team.Spec.ExternalID},
		}
		assertErrCode(t, "creating a second Team for the same external id", f.st.CreateTeam(dup), "23505")

		spec := *team.Spec
		spec.ExternalID = "another-id"
		assertErrCode(t, "changing a Team's external_id", f.st.UpdateTeam(team.GetID(), &v1.Team{Spec: &spec}), "10273")
	})
}

func TestOrgUnit_Inactive(t *testing.T) {
	f := newOrgFixture(t)
	a := f.createOrgUnit("")

	if err := f.st.UpdateOrgUnit(a.GetID(), &v1.OrgUnit{Status: &v1.OrgUnitStatus{Phase: v1.OrgPhaseInactive}}); err != nil {
		t.Fatalf("deactivating failed: %v", err)
	}

	got := f.getOrgUnit(a.Metadata.Name)
	if got.IsActive() || got.Status.LastTransitionTime == "" || got.ID != a.ID {
		t.Fatalf("after deactivation: %+v (id %d), want Inactive with a transition time and the same id", got.Status, got.ID)
	}

	t.Run("a status without phase keeps the phase", func(t *testing.T) {
		if err := f.st.UpdateOrgUnit(a.GetID(), &v1.OrgUnit{Status: &v1.OrgUnitStatus{ErrorMessage: "x"}}); err != nil {
			t.Fatalf("status update failed: %v", err)
		}

		if got := f.getOrgUnit(a.Metadata.Name); got.IsActive() {
			t.Errorf("phase = %s, want Inactive", got.Status.Phase)
		}
	})

	t.Run("unknown phases are rejected", func(t *testing.T) {
		err := f.st.UpdateOrgUnit(a.GetID(), &v1.OrgUnit{Status: &v1.OrgUnitStatus{Phase: "Deleted"}})
		assertErrCode(t, "setting phase Deleted", err, "10274")
	})

	t.Run("users cannot be put into an inactive OrgUnit", func(t *testing.T) {
		user := createGoTrueTestUser(t, "org-inactive", "password123")
		err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: user.ID, OrgUnitID: a.ID, IdentitySource: f.source})
		assertErrCode(t, "assigning to an inactive OrgUnit", err, "10277")
	})

	t.Run("a Team can be deactivated and reactivated", func(t *testing.T) {
		team := f.createTeam()

		for _, phase := range []v1.OrgPhase{v1.OrgPhaseInactive, v1.OrgPhaseActive} {
			if err := f.st.UpdateTeam(team.GetID(), &v1.Team{Status: &v1.TeamStatus{Phase: phase}}); err != nil {
				t.Fatalf("setting Team phase %s failed: %v", phase, err)
			}

			got, err := f.st.GetTeam(team.GetID())
			if err != nil || got.Status.Phase != phase {
				t.Fatalf("Team after setting %s: %+v, %v", phase, got, err)
			}
		}
	})
}

func TestOrgUnit_SubtreeUsesPathIndex(t *testing.T) {
	f := newOrgFixture(t)
	a := f.createOrgUnit("")
	f.createOrgUnit(a.Metadata.Name)

	db := GetTestDB(t)
	ctx := context.Background()

	for _, query := range []string{
		fmt.Sprintf(`SELECT * FROM api.org_unit_subtree(%d)`, a.ID),
		fmt.Sprintf(`SELECT * FROM api.org_unit_subtree_members(%d)`, a.ID),
		fmt.Sprintf(`SELECT * FROM api.org_units WHERE path LIKE '%s%%'`, a.Path),
	} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin failed: %v", err)
		}

		// The tables are tiny; take sequential scans off the table so the
		// plan shows whether the index can serve the query at all.
		if _, err := tx.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
			t.Fatalf("SET failed: %v", err)
		}

		rows, err := tx.QueryContext(ctx, "EXPLAIN "+query)
		if err != nil {
			t.Fatalf("EXPLAIN %s failed: %v", query, err)
		}

		var plan []string

		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan failed: %v", err)
			}

			plan = append(plan, line)
		}

		rows.Close()
		_ = tx.Rollback()

		if !strings.Contains(strings.Join(plan, "\n"), "org_units_path_idx") {
			t.Errorf("plan of %s does not use org_units_path_idx:\n%s", query, strings.Join(plan, "\n"))
		}
	}
}

func TestOrgUnit_Members(t *testing.T) {
	f := newOrgFixture(t)
	a := f.createOrgUnit("")
	b := f.createOrgUnit(a.Metadata.Name)
	c := f.createOrgUnit(b.Metadata.Name)

	u1 := createGoTrueTestUser(t, "org-member-1", "password123")
	u2 := createGoTrueTestUser(t, "org-member-2", "password123")

	set := func(user string, unit *v1.OrgUnit) {
		t.Helper()

		if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: user, OrgUnitID: unit.ID, IdentitySource: f.source}); err != nil {
			t.Fatalf("SetOrgUnitMember(%s, %s) failed: %v", user, unit.Metadata.Name, err)
		}
	}

	members := func(user string) []storage.OrgUnitMember {
		t.Helper()

		rows, err := f.st.ListOrgUnitMember(storage.ListOption{Filters: []storage.Filter{{Column: "user_id", Operator: "eq", Value: user}}})
		if err != nil {
			t.Fatalf("ListOrgUnitMember failed: %v", err)
		}

		return rows
	}

	t.Run("a user has one primary OrgUnit", func(t *testing.T) {
		set(u1.ID, b)
		set(u1.ID, c)

		rows := members(u1.ID)
		if len(rows) != 1 || rows[0].OrgUnitID != c.ID || rows[0].IdentitySource != f.source {
			t.Fatalf("memberships of u1 = %+v, want one in %d from %s", rows, c.ID, f.source)
		}

		db := GetTestDB(t)
		_, err := db.Exec(`INSERT INTO api.org_unit_members (user_id, org_unit_id, identity_source) VALUES ($1, $2, $3)`, u1.ID, a.ID, f.source)
		assertErrCode(t, "a second OrgUnit row for u1", err, "org_unit_members_pkey")
	})

	t.Run("subtree members", func(t *testing.T) {
		set(u2.ID, b)

		got, err := f.st.ListOrgUnitSubtreeMembers(a.ID)
		if err != nil {
			t.Fatalf("ListOrgUnitSubtreeMembers failed: %v", err)
		}

		users := []string{}
		for _, m := range got {
			users = append(users, m.UserID)
		}

		sort.Strings(users)

		want := []string{u1.ID, u2.ID}
		sort.Strings(want)

		if fmt.Sprint(users) != fmt.Sprint(want) {
			t.Errorf("subtree members of a = %v, want %v", users, want)
		}

		got, err = f.st.ListOrgUnitSubtreeMembers(c.ID)
		if err != nil || len(got) != 1 || got[0].UserID != u1.ID {
			t.Errorf("subtree members of c = %+v, %v; want only u1", got, err)
		}
	})

	t.Run("members move with their OrgUnit", func(t *testing.T) {
		if err := f.moveOrgUnit(c, ""); err != nil {
			t.Fatalf("move c to the root failed: %v", err)
		}

		got, err := f.st.ListOrgUnitSubtreeMembers(a.ID)
		if err != nil || len(got) != 1 || got[0].UserID != u2.ID {
			t.Errorf("subtree members of a after moving c out = %+v, %v; want only u2", got, err)
		}
	})

	t.Run("a user can be in several Teams", func(t *testing.T) {
		t1 := f.createTeam()
		t2 := f.createTeam()

		for _, team := range []*v1.Team{t1, t2, t1} {
			if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: team.ID, UserID: u1.ID}); err != nil {
				t.Fatalf("AddTeamMember(%d) failed: %v", team.ID, err)
			}
		}

		rows, err := f.st.ListTeamMember(storage.ListOption{Filters: []storage.Filter{{Column: "user_id", Operator: "eq", Value: u1.ID}}})
		if err != nil || len(rows) != 2 {
			t.Fatalf("Team memberships of u1 = %+v, %v; want 2", rows, err)
		}

		if err := f.st.DeleteTeamMember(t1.ID, u1.ID); err != nil {
			t.Fatalf("DeleteTeamMember failed: %v", err)
		}

		rows, err = f.st.ListTeamMember(storage.ListOption{Filters: []storage.Filter{{Column: "user_id", Operator: "eq", Value: u1.ID}}})
		if err != nil || len(rows) != 1 || rows[0].TeamID != t2.ID {
			t.Fatalf("Team memberships of u1 after removal = %+v, %v; want only %d", rows, err, t2.ID)
		}
	})

	t.Run("a deleted user leaves no membership", func(t *testing.T) {
		u3 := createLDAPShapedUser(t, newExternalID())
		set(u3, a)
		deleteGoTrueUser(t, u3)

		if rows := members(u3); len(rows) != 0 {
			t.Errorf("memberships of a deleted user = %+v, want none", rows)
		}
	})
}

func TestOrgModel_RLS(t *testing.T) {
	f := newOrgFixture(t)
	a := f.createOrgUnit("")
	b := f.createOrgUnit(a.Metadata.Name)
	team := f.createTeam()

	synced := createGoTrueTestUser(t, "org-rls-synced", "password123")
	if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: synced.ID, OrgUnitID: a.ID, IdentitySource: f.source}); err != nil {
		t.Fatalf("SetOrgUnitMember failed: %v", err)
	}

	if err := f.st.AddTeamMember(&storage.TeamMember{TeamID: team.ID, UserID: synced.ID}); err != nil {
		t.Fatalf("AddTeamMember failed: %v", err)
	}

	local := createGoTrueTestUser(t, "org-rls-local", "password123")

	external := createLDAPShapedUser(t, newExternalID())
	t.Cleanup(func() { deleteGoTrueUser(t, external) })

	if err := f.st.CreateExternalIdentity(&storage.ExternalIdentity{Source: "ldap:" + f.source, ExternalID: newExternalID(), UserID: external}); err != nil {
		t.Fatalf("CreateExternalIdentity failed: %v", err)
	}

	newUnitBody := func() string {
		name := newOrgName("ou")
		return mustJSON(f.orgUnit(name, "ext-"+name, ""))
	}

	count := func(t *testing.T, token, path string) int {
		t.Helper()

		code, raw := postgrestAs(t, token, http.MethodGet, path, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s = HTTP %d: %s", path, code, raw)
		}

		var rows []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &rows); err != nil {
			t.Fatalf("GET %s returned %q: %v", path, raw, err)
		}

		return len(rows)
	}

	unitFilter := "/org_units?spec->>identity_source=eq." + f.source
	teamFilter := "/teams?spec->>identity_source=eq." + f.source
	memberFilter := fmt.Sprintf("/org_unit_members?org_unit_id=in.(%d,%d)", a.ID, b.ID)
	teamMemberFilter := fmt.Sprintf("/team_members?team_id=eq.%d", team.ID)

	t.Run("no permission reads nothing and writes nothing", func(t *testing.T) {
		token := userTokenWithPermissions(t, "org-none", nil)

		for _, path := range []string{unitFilter, teamFilter, memberFilter, teamMemberFilter} {
			if n := count(t, token, path); n != 0 {
				t.Errorf("GET %s returned %d rows, want 0", path, n)
			}
		}

		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_units", newUnitBody()); code != http.StatusForbidden {
			t.Errorf("POST /org_units = HTTP %d, want 403: %s", code, raw)
		}
	})

	t.Run("readers read but cannot write", func(t *testing.T) {
		token := userTokenWithPermissions(t, "org-reader", []string{"org_unit:read", "team:read"})

		if n := count(t, token, unitFilter); n != 2 {
			t.Errorf("reader sees %d OrgUnits, want 2", n)
		}

		if n := count(t, token, teamFilter); n != 1 {
			t.Errorf("reader sees %d Teams, want 1", n)
		}

		if n := count(t, token, memberFilter); n != 1 {
			t.Errorf("reader sees %d OrgUnit members, want 1", n)
		}

		if n := count(t, token, teamMemberFilter); n != 1 {
			t.Errorf("reader sees %d Team members, want 1", n)
		}

		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_units", newUnitBody()); code != http.StatusForbidden {
			t.Errorf("POST /org_units = HTTP %d, want 403: %s", code, raw)
		}

		patch := mustJSON(map[string]any{"status": map[string]any{"phase": "Inactive"}})
		if _, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/org_units?id=eq.%d", a.ID), patch); strings.Contains(raw, `"id"`) {
			t.Errorf("PATCH /org_units changed a row: %s", raw)
		}

		if !f.getOrgUnit(a.Metadata.Name).IsActive() {
			t.Errorf("a reader deactivated an OrgUnit")
		}

		body := mustJSON(map[string]any{"team_id": team.ID, "user_id": local.ID})
		if code, raw := postgrestAs(t, token, http.MethodPost, "/team_members", body); code != http.StatusForbidden {
			t.Errorf("POST /team_members = HTTP %d, want 403: %s", code, raw)
		}

		body = mustJSON(map[string]any{"org_unit_id": a.ID, "user_id": local.ID})
		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_unit_members", body); code != http.StatusForbidden {
			t.Errorf("POST /org_unit_members without org_unit:assign-member = HTTP %d, want 403: %s", code, raw)
		}
	})

	t.Run("assigners put local accounts into existing OrgUnits", func(t *testing.T) {
		token := userTokenWithPermissions(t, "org-assigner", []string{"org_unit:read", "org_unit:assign-member"})

		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_units", newUnitBody()); code != http.StatusForbidden {
			t.Errorf("POST /org_units = HTTP %d, want 403: %s", code, raw)
		}

		body := mustJSON(map[string]any{"org_unit_id": a.ID, "user_id": local.ID})
		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_unit_members", body); code != http.StatusCreated {
			t.Fatalf("POST /org_unit_members for a local account = HTTP %d, want 201: %s", code, raw)
		}

		body = mustJSON(map[string]any{"org_unit_id": b.ID})
		if code, raw := postgrestAs(t, token, http.MethodPatch, "/org_unit_members?user_id=eq."+local.ID, body); code != http.StatusOK || !strings.Contains(raw, local.ID) {
			t.Errorf("PATCH /org_unit_members for a local account = HTTP %d: %s", code, raw)
		}

		// Rows sync wrote stay out of reach.
		body = mustJSON(map[string]any{"org_unit_id": b.ID})
		if _, raw := postgrestAs(t, token, http.MethodPatch, "/org_unit_members?user_id=eq."+synced.ID, body); strings.Contains(raw, synced.ID) {
			t.Errorf("PATCH changed a synced membership: %s", raw)
		}

		if _, raw := postgrestAs(t, token, http.MethodDelete, "/org_unit_members?user_id=eq."+synced.ID, ""); strings.Contains(raw, synced.ID) {
			t.Errorf("DELETE removed a synced membership: %s", raw)
		}

		if rows, err := f.st.ListOrgUnitMember(storage.ListOption{Filters: []storage.Filter{{Column: "user_id", Operator: "eq", Value: synced.ID}}}); err != nil || len(rows) != 1 || rows[0].OrgUnitID != a.ID {
			t.Errorf("synced membership after the attempts = %+v, %v; want it in %d", rows, err, a.ID)
		}

		body = mustJSON(map[string]any{"org_unit_id": a.ID, "user_id": external, "identity_source": f.source})
		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_unit_members", body); code != http.StatusForbidden {
			t.Errorf("POST /org_unit_members claiming a sync source = HTTP %d, want 403: %s", code, raw)
		}

		body = mustJSON(map[string]any{"org_unit_id": a.ID, "user_id": external})
		if code, raw := postgrestAs(t, token, http.MethodPost, "/org_unit_members", body); code != http.StatusBadRequest || !strings.Contains(raw, "10278") {
			t.Errorf("POST /org_unit_members for an external account = HTTP %d, want 400 10278: %s", code, raw)
		}

		if code, raw := postgrestAs(t, token, http.MethodDelete, "/org_unit_members?user_id=eq."+local.ID, ""); code != http.StatusOK || !strings.Contains(raw, local.ID) {
			t.Errorf("DELETE /org_unit_members for a local account = HTTP %d: %s", code, raw)
		}
	})

	t.Run("sync can take over a hand assignment", func(t *testing.T) {
		if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: local.ID, OrgUnitID: a.ID}); err != nil {
			t.Fatalf("hand assignment as service_role failed: %v", err)
		}

		if err := f.st.SetOrgUnitMember(&storage.OrgUnitMember{UserID: local.ID, OrgUnitID: b.ID, IdentitySource: f.source}); err != nil {
			t.Fatalf("sync takeover failed: %v", err)
		}

		rows, err := f.st.ListOrgUnitMember(storage.ListOption{Filters: []storage.Filter{{Column: "user_id", Operator: "eq", Value: local.ID}}})
		if err != nil || len(rows) != 1 || rows[0].OrgUnitID != b.ID || rows[0].IdentitySource != f.source {
			t.Errorf("membership after takeover = %+v, %v", rows, err)
		}
	})

	t.Run("admin reads everything", func(t *testing.T) {
		token := adminToken(t)

		if n := count(t, token, unitFilter); n != 2 {
			t.Errorf("admin sees %d OrgUnits, want 2", n)
		}

		if n := count(t, token, teamFilter); n != 1 {
			t.Errorf("admin sees %d Teams, want 1", n)
		}
	})
}

func TestOrgModel_PresetRolePermissions(t *testing.T) {
	db := GetTestDB(t)

	perms := func(role string) string {
		t.Helper()

		var p string
		if err := db.QueryRow(`SELECT (spec).permissions::text FROM api.roles WHERE (metadata).name = $1`, role).Scan(&p); err != nil {
			t.Fatalf("reading %s permissions failed: %v", role, err)
		}

		return p
	}

	admin := perms("admin")
	for _, p := range []string{"org_unit:read", "org_unit:assign-member", "team:read"} {
		if !strings.Contains(admin, p) {
			t.Errorf("admin lacks %s: %s", p, admin)
		}
	}

	if wsUser := perms("workspace-user"); strings.Contains(wsUser, "org_unit:") || strings.Contains(wsUser, "team:") {
		t.Errorf("workspace-user holds organization permissions: %s", wsUser)
	}
}
