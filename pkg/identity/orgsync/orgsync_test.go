package orgsync

import (
	"go/build"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func baseSnapshot() *Snapshot {
	return &Snapshot{
		OrgUnits: []OrgUnit{
			{ExternalID: "plat", ParentExternalID: "eng", DisplayName: "Platform"},
			{ExternalID: "eng", ParentExternalID: "root", DisplayName: "Engineering"},
			{ExternalID: "root", DisplayName: "Company"},
		},
		Teams: []Team{{ExternalID: "t-dev", DisplayName: "Developers"}},
		Users: []User{
			{ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice", OrgUnitExternalID: "plat", TeamExternalIDs: []string{"t-dev"}},
		},
	}
}

// stateOf returns the state a successful apply of the snapshot would leave.
func stateOf(s *Snapshot) *State {
	st := &State{}
	for _, ou := range s.OrgUnits {
		st.OrgUnits = append(st.OrgUnits, OrgUnitState{ExternalID: ou.ExternalID, ParentExternalID: ou.ParentExternalID, DisplayName: ou.DisplayName, Active: true})
	}

	for _, t := range s.Teams {
		st.Teams = append(st.Teams, TeamState{ExternalID: t.ExternalID, DisplayName: t.DisplayName, Active: true})
	}

	for _, u := range s.Users {
		if u.Disabled {
			continue
		}

		st.Users = append(st.Users, UserState{
			ExternalID: u.ExternalID, Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Active: true,
			OrgUnitExternalID: u.OrgUnitExternalID, TeamExternalIDs: u.TeamExternalIDs,
		})
	}

	return st
}

func TestDiffInitialSync(t *testing.T) {
	snap := baseSnapshot()
	snap.Users = append(snap.Users,
		User{ExternalID: "u-new", Username: "new", DisplayName: "New"},
		User{ExternalID: "u-off", Username: "off", Disabled: true, OrgUnitExternalID: "eng", TeamExternalIDs: []string{"t-dev"}},
	)

	plan, err := Diff(snap, nil)
	require.NoError(t, err)

	assert.Equal(t, []OrgUnitChange{
		{Action: ActionCreate, ExternalID: "root", DisplayName: "Company"},
		{Action: ActionCreate, ExternalID: "eng", ParentExternalID: "root", DisplayName: "Engineering"},
		{Action: ActionCreate, ExternalID: "plat", ParentExternalID: "eng", DisplayName: "Platform"},
	}, plan.OrgUnits, "parents before children")
	assert.Equal(t, []TeamChange{{Action: ActionCreate, ExternalID: "t-dev", DisplayName: "Developers"}}, plan.Teams)
	assert.Equal(t, []UserChange{
		{Action: ActionCreate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"},
		{Action: ActionCreate, ExternalID: "u-new", Username: "new", DisplayName: "New"},
	}, plan.Users, "users that never logged in are created; a disabled one is not")
	assert.Equal(t, []OrgUnitMembershipChange{{UserExternalID: "u-alice", OrgUnitExternalID: "plat"}}, plan.OrgUnitMemberships)
	assert.Equal(t, []TeamMembershipChange{{UserExternalID: "u-alice", TeamExternalID: "t-dev"}}, plan.TeamMemberships)
}

func TestDiffNoChange(t *testing.T) {
	snap := baseSnapshot()
	plan, err := Diff(snap, stateOf(snap))
	require.NoError(t, err)
	assert.True(t, plan.Empty(), "%+v", plan)
}

func TestDiffRenameAndMove(t *testing.T) {
	snap := baseSnapshot()
	state := stateOf(snap)

	// eng renamed; plat moves to a new root "ops" that is created in the same plan
	snap.OrgUnits[1].DisplayName = "R&D"
	snap.OrgUnits[0].ParentExternalID = "ops"
	snap.OrgUnits = append(snap.OrgUnits, OrgUnit{ExternalID: "ops", DisplayName: "Ops"})
	snap.Teams[0].DisplayName = "Devs"
	snap.Users[0].DisplayName = "Alice L."

	plan, err := Diff(snap, state)
	require.NoError(t, err)

	assert.Equal(t, []OrgUnitChange{
		{Action: ActionCreate, ExternalID: "ops", DisplayName: "Ops"},
		{Action: ActionUpdate, ExternalID: "plat", ParentExternalID: "ops", DisplayName: "Platform"},
		{Action: ActionUpdate, ExternalID: "eng", ParentExternalID: "root", DisplayName: "R&D"},
	}, plan.OrgUnits, "breadth-first: roots ops, root; then their children")
	assert.Equal(t, []TeamChange{{Action: ActionUpdate, ExternalID: "t-dev", DisplayName: "Devs"}}, plan.Teams)
	assert.Equal(t, []UserChange{{Action: ActionUpdate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice L."}}, plan.Users)
	assert.Empty(t, plan.OrgUnitMemberships)
	assert.Empty(t, plan.TeamMemberships)
}

func TestDiffInvertedTreeHasNoCycleStep(t *testing.T) {
	// state: root > eng; snapshot: eng > root. eng must become a root before
	// root is put under it.
	state := &State{OrgUnits: []OrgUnitState{
		{ExternalID: "root", DisplayName: "Root", Active: true},
		{ExternalID: "eng", ParentExternalID: "root", DisplayName: "Eng", Active: true},
	}}
	snap := &Snapshot{OrgUnits: []OrgUnit{
		{ExternalID: "root", ParentExternalID: "eng", DisplayName: "Root"},
		{ExternalID: "eng", DisplayName: "Eng"},
	}}

	plan, err := Diff(snap, state)
	require.NoError(t, err)
	assert.Equal(t, []OrgUnitChange{
		{Action: ActionUpdate, ExternalID: "eng", DisplayName: "Eng"},
		{Action: ActionUpdate, ExternalID: "root", ParentExternalID: "eng", DisplayName: "Root"},
	}, plan.OrgUnits)
}

func TestDiffDeactivateAndReactivate(t *testing.T) {
	snap := baseSnapshot()
	state := stateOf(snap)

	// the directory loses plat, the team and alice
	gone := &Snapshot{OrgUnits: snap.OrgUnits[1:]}

	plan, err := Diff(gone, state)
	require.NoError(t, err)
	assert.Equal(t, []OrgUnitChange{{Action: ActionDeactivate, ExternalID: "plat", ParentExternalID: "eng", DisplayName: "Platform"}}, plan.OrgUnits)
	assert.Equal(t, []TeamChange{{Action: ActionDeactivate, ExternalID: "t-dev", DisplayName: "Developers"}}, plan.Teams)
	assert.Equal(t, []UserChange{{Action: ActionDeactivate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"}}, plan.Users)
	assert.Equal(t, []OrgUnitMembershipChange{{UserExternalID: "u-alice"}}, plan.OrgUnitMemberships)
	assert.Equal(t, []TeamMembershipChange{{UserExternalID: "u-alice", TeamExternalID: "t-dev", Remove: true}}, plan.TeamMemberships)

	// after applying: everything inactive, memberships gone
	after := stateOf(gone)
	after.OrgUnits = append(after.OrgUnits, OrgUnitState{ExternalID: "plat", ParentExternalID: "eng", DisplayName: "Platform"})
	after.Teams = []TeamState{{ExternalID: "t-dev", DisplayName: "Developers"}}
	after.Users = []UserState{{ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"}}

	plan, err = Diff(gone, after)
	require.NoError(t, err)
	assert.True(t, plan.Empty(), "inactive objects are not deactivated again: %+v", plan)

	// everything comes back
	plan, err = Diff(snap, after)
	require.NoError(t, err)
	assert.Equal(t, []OrgUnitChange{{Action: ActionReactivate, ExternalID: "plat", ParentExternalID: "eng", DisplayName: "Platform"}}, plan.OrgUnits)
	assert.Equal(t, []TeamChange{{Action: ActionReactivate, ExternalID: "t-dev", DisplayName: "Developers"}}, plan.Teams)
	assert.Equal(t, []UserChange{{Action: ActionReactivate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"}}, plan.Users)
	assert.Equal(t, []OrgUnitMembershipChange{{UserExternalID: "u-alice", OrgUnitExternalID: "plat"}}, plan.OrgUnitMemberships)
	assert.Equal(t, []TeamMembershipChange{{UserExternalID: "u-alice", TeamExternalID: "t-dev"}}, plan.TeamMemberships)
}

func TestDiffDeactivationsComeAfterWrites(t *testing.T) {
	state := &State{OrgUnits: []OrgUnitState{{ExternalID: "old", DisplayName: "Old", Active: true}}}
	snap := &Snapshot{OrgUnits: []OrgUnit{{ExternalID: "new", DisplayName: "New"}}}

	plan, err := Diff(snap, state)
	require.NoError(t, err)
	require.Len(t, plan.OrgUnits, 2)
	assert.Equal(t, ActionCreate, plan.OrgUnits[0].Action)
	assert.Equal(t, ActionDeactivate, plan.OrgUnits[1].Action)
}

func TestDiffDisabledUser(t *testing.T) {
	snap := baseSnapshot()
	state := stateOf(snap)
	snap.Users[0].Disabled = true

	plan, err := Diff(snap, state)
	require.NoError(t, err)
	assert.Equal(t, []UserChange{{Action: ActionDeactivate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"}}, plan.Users)
	assert.Empty(t, plan.OrgUnitMemberships, "a disabled user keeps the memberships the directory gives it")
	assert.Empty(t, plan.TeamMemberships)

	state.Users[0].Active = false
	plan, err = Diff(snap, state)
	require.NoError(t, err)
	assert.True(t, plan.Empty(), "already inactive: %+v", plan)

	snap.Users[0].Disabled = false
	plan, err = Diff(snap, state)
	require.NoError(t, err)
	assert.Equal(t, []UserChange{{Action: ActionReactivate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"}}, plan.Users)
}

func TestDiffMemberships(t *testing.T) {
	snap := baseSnapshot()
	snap.Teams = append(snap.Teams, Team{ExternalID: "t-ops", DisplayName: "Ops"}, Team{ExternalID: "t-sec", DisplayName: "Sec"})
	state := stateOf(snap)
	state.Users[0].TeamExternalIDs = []string{"t-dev", "t-sec"}

	snap.Users[0].OrgUnitExternalID = "eng"
	snap.Users[0].TeamExternalIDs = []string{"t-ops", "t-dev"}

	plan, err := Diff(snap, state)
	require.NoError(t, err)
	assert.Empty(t, plan.Users)
	assert.Equal(t, []OrgUnitMembershipChange{{UserExternalID: "u-alice", OrgUnitExternalID: "eng"}}, plan.OrgUnitMemberships)
	assert.Equal(t, []TeamMembershipChange{
		{UserExternalID: "u-alice", TeamExternalID: "t-ops"},
		{UserExternalID: "u-alice", TeamExternalID: "t-sec", Remove: true},
	}, plan.TeamMemberships)

	// leaving every department
	snap.Users[0].OrgUnitExternalID = ""
	plan, err = Diff(snap, state)
	require.NoError(t, err)
	assert.Equal(t, []OrgUnitMembershipChange{{UserExternalID: "u-alice"}}, plan.OrgUnitMemberships)
}

func TestDiffMatchesByExternalIDOnly(t *testing.T) {
	snap := baseSnapshot()
	state := stateOf(snap)
	// same username and email, different directory object
	snap.Users[0].ExternalID = "u-alice-2"

	plan, err := Diff(snap, state)
	require.NoError(t, err)
	assert.Equal(t, []UserChange{
		{Action: ActionCreate, ExternalID: "u-alice-2", Username: "alice", Email: "alice@x", DisplayName: "Alice"},
		{Action: ActionDeactivate, ExternalID: "u-alice", Username: "alice", Email: "alice@x", DisplayName: "Alice"},
	}, plan.Users)
}

func TestDiffEmptySnapshotDeactivatesAll(t *testing.T) {
	snap := baseSnapshot()

	plan, err := Diff(&Snapshot{}, stateOf(snap))
	require.NoError(t, err)
	assert.Len(t, plan.OrgUnits, 3)
	assert.Len(t, plan.Teams, 1)
	assert.Len(t, plan.Users, 1)

	for _, c := range plan.OrgUnits {
		assert.Equal(t, ActionDeactivate, c.Action)
	}
}

func TestDiffInvalidInput(t *testing.T) {
	cases := map[string]func(*Snapshot, *State){
		"empty id":            func(s *Snapshot, _ *State) { s.Users[0].ExternalID = "" },
		"duplicate org unit":  func(s *Snapshot, _ *State) { s.OrgUnits = append(s.OrgUnits, s.OrgUnits[0]) },
		"unknown parent":      func(s *Snapshot, _ *State) { s.OrgUnits[0].ParentExternalID = "nope" },
		"self parent":         func(s *Snapshot, _ *State) { s.OrgUnits[2].ParentExternalID = "root" },
		"unknown user ou":     func(s *Snapshot, _ *State) { s.Users[0].OrgUnitExternalID = "nope" },
		"unknown user team":   func(s *Snapshot, _ *State) { s.Users[0].TeamExternalIDs = []string{"nope"} },
		"duplicate state":     func(_ *Snapshot, st *State) { st.Teams = append(st.Teams, st.Teams[0]) },
		"cycle":               func(s *Snapshot, _ *State) { s.OrgUnits[2].ParentExternalID = "plat" },
		"empty state user id": func(_ *Snapshot, st *State) { st.Users[0].ExternalID = "" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snap := baseSnapshot()
			state := stateOf(baseSnapshot())
			mutate(snap, state)

			plan, err := Diff(snap, state)
			assert.Nil(t, plan)
			assert.ErrorIs(t, err, ErrInvalidInput)
		})
	}

	_, err := Diff(nil, nil)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// TestStdlibOnly keeps the package reusable outside neutree.
func TestStdlibOnly(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)

	for _, path := range pkg.Imports {
		assert.False(t, strings.Contains(strings.SplitN(path, "/", 2)[0], "."), "unexpected import %q", path)
	}
}
