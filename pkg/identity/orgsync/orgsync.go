// Package orgsync computes how to bring the organization stored by an
// application (departments, teams, users and their memberships) in line with a
// directory.
//
// A directory reader (for example pkg/identity/ldap) produces a Snapshot: the
// whole directory, read in one pass. The caller loads the State it currently
// stores for the same identity source. Diff compares the two and returns a
// Plan of writes. Diff is a pure function; it does no I/O.
//
// Objects are matched only by their directory external ID, never by name,
// username or email. Nothing is ever deleted: an object that is gone from the
// directory is deactivated, and it is reactivated when it comes back.
//
// The package imports only the standard library, so it can be reused outside
// neutree.
package orgsync

import (
	"errors"
	"fmt"
	"slices"
	"sort"
)

// ErrInvalidInput means a Snapshot or a State is not consistent: an empty or
// duplicate external ID, or a reference to an object that is not in it.
var ErrInvalidInput = errors.New("orgsync: invalid input")

// Snapshot is a complete read of one directory. A reader returns either a
// complete Snapshot or an error, never a partial one: Diff treats every object
// that is missing from it as gone from the directory.
type Snapshot struct {
	OrgUnits []OrgUnit
	Teams    []Team
	Users    []User
}

// OrgUnit is a department in the directory.
type OrgUnit struct {
	ExternalID string
	// ParentExternalID is the ExternalID of the parent OrgUnit; empty for a root.
	ParentExternalID string
	DisplayName      string
	// DN is informational (logs, debugging); Diff ignores it.
	DN string
}

// Team is a directory group. Nested groups are already expanded: membership is
// listed on each User as direct membership.
type Team struct {
	ExternalID  string
	DisplayName string
	DN          string
}

// User is a user in the directory, whether or not it has ever logged in.
type User struct {
	ExternalID  string
	Username    string
	Email       string
	DisplayName string
	// OrgUnitExternalID is the user's primary department; empty for none.
	OrgUnitExternalID string
	// TeamExternalIDs lists every Team the user is in, nested groups expanded.
	TeamExternalIDs []string
	// Disabled is true when the directory marks the account as disabled.
	Disabled bool
	DN       string
}

// State is what the application stores today for one identity source. Every
// object in it was written by an earlier sync of that source.
type State struct {
	OrgUnits []OrgUnitState
	Teams    []TeamState
	Users    []UserState
}

// OrgUnitState is a stored department.
type OrgUnitState struct {
	ExternalID       string
	ParentExternalID string
	DisplayName      string
	// Active is false when an earlier sync deactivated the department.
	Active bool
}

// TeamState is a stored team.
type TeamState struct {
	ExternalID  string
	DisplayName string
	Active      bool
}

// UserState is a stored user linked to the identity source.
type UserState struct {
	ExternalID  string
	Username    string
	Email       string
	DisplayName string
	// Active is false when an earlier sync deactivated the user.
	Active bool
	// OrgUnitExternalID is the stored primary department; empty for none.
	OrgUnitExternalID string
	TeamExternalIDs   []string
}

// Action is what to do with one object.
type Action string

const (
	// ActionCreate writes a new object, active.
	ActionCreate Action = "Create"
	// ActionUpdate rewrites an active object whose fields changed.
	ActionUpdate Action = "Update"
	// ActionReactivate sets an inactive object active again and writes its
	// current fields.
	ActionReactivate Action = "Reactivate"
	// ActionDeactivate sets an object inactive. It is never deleted.
	ActionDeactivate Action = "Deactivate"
)

// OrgUnitChange carries the desired fields of the department. For
// ActionDeactivate the fields are the stored ones.
type OrgUnitChange struct {
	Action           Action
	ExternalID       string
	ParentExternalID string
	DisplayName      string
}

// TeamChange carries the desired fields of the team.
type TeamChange struct {
	Action      Action
	ExternalID  string
	DisplayName string
}

// UserChange carries the desired fields of the user. For ActionDeactivate the
// fields are the stored ones.
type UserChange struct {
	Action      Action
	ExternalID  string
	Username    string
	Email       string
	DisplayName string
}

// OrgUnitMembershipChange sets a user's primary department.
// An empty OrgUnitExternalID removes it.
type OrgUnitMembershipChange struct {
	UserExternalID    string
	OrgUnitExternalID string
}

// TeamMembershipChange adds a user to a team or removes the user from it.
type TeamMembershipChange struct {
	UserExternalID string
	TeamExternalID string
	// Remove is false to add the membership, true to remove it.
	Remove bool
}

// Plan is the writes that bring a State in line with a Snapshot. Apply the
// lists in field order: OrgUnits, Teams, Users, OrgUnitMemberships,
// TeamMemberships. Each list is in the order to apply it.
type Plan struct {
	// OrgUnits lists Create, Update and Reactivate first, parents before
	// children, then Deactivate.
	OrgUnits []OrgUnitChange
	Teams    []TeamChange
	Users    []UserChange
	// OrgUnitMemberships refer only to users that exist after Users is applied
	// and to departments that are active in the Snapshot (or to none).
	OrgUnitMemberships []OrgUnitMembershipChange
	TeamMemberships    []TeamMembershipChange
}

// Empty reports whether the plan has no writes.
func (p *Plan) Empty() bool {
	return len(p.OrgUnits) == 0 && len(p.Teams) == 0 && len(p.Users) == 0 &&
		len(p.OrgUnitMemberships) == 0 && len(p.TeamMemberships) == 0
}

// Diff returns the writes that make state match snapshot.
//
// Rules:
//   - Departments and teams: missing from the state → Create; changed name or
//     parent → Update; inactive in the state → Reactivate; missing from the
//     snapshot and active in the state → Deactivate.
//   - Users: a user in the snapshot that is not in the state is created even
//     though it never logged in, unless the directory disables it. A disabled
//     user, or a user missing from the snapshot, is deactivated; an inactive
//     user that is enabled in the directory is reactivated. Username, email and
//     display name follow the directory.
//   - Memberships follow the directory for every user in the snapshot that
//     exists after the plan. A user missing from the snapshot loses its
//     department and all its teams.
func Diff(snapshot *Snapshot, state *State) (*Plan, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("%w: nil snapshot", ErrInvalidInput)
	}

	if state == nil {
		state = &State{}
	}

	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}

	if err := validateState(state); err != nil {
		return nil, err
	}

	plan := &Plan{}

	orgUnits, err := diffOrgUnits(snapshot.OrgUnits, state.OrgUnits)
	if err != nil {
		return nil, err
	}

	plan.OrgUnits = orgUnits
	plan.Teams = diffTeams(snapshot.Teams, state.Teams)
	plan.Users, plan.OrgUnitMemberships, plan.TeamMemberships = diffUsers(snapshot.Users, state.Users)

	return plan, nil
}

func validateSnapshot(s *Snapshot) error {
	orgUnits := make(map[string]bool, len(s.OrgUnits))
	for _, ou := range s.OrgUnits {
		if err := addID(orgUnits, "snapshot org unit", ou.ExternalID); err != nil {
			return err
		}
	}

	for _, ou := range s.OrgUnits {
		if ou.ParentExternalID != "" && !orgUnits[ou.ParentExternalID] {
			return fmt.Errorf("%w: snapshot org unit %q has unknown parent %q", ErrInvalidInput, ou.ExternalID, ou.ParentExternalID)
		}

		if ou.ParentExternalID == ou.ExternalID {
			return fmt.Errorf("%w: snapshot org unit %q is its own parent", ErrInvalidInput, ou.ExternalID)
		}
	}

	teams := make(map[string]bool, len(s.Teams))
	for _, t := range s.Teams {
		if err := addID(teams, "snapshot team", t.ExternalID); err != nil {
			return err
		}
	}

	users := make(map[string]bool, len(s.Users))
	for _, u := range s.Users {
		if err := addID(users, "snapshot user", u.ExternalID); err != nil {
			return err
		}

		if u.OrgUnitExternalID != "" && !orgUnits[u.OrgUnitExternalID] {
			return fmt.Errorf("%w: snapshot user %q has unknown org unit %q", ErrInvalidInput, u.ExternalID, u.OrgUnitExternalID)
		}

		for _, t := range u.TeamExternalIDs {
			if !teams[t] {
				return fmt.Errorf("%w: snapshot user %q has unknown team %q", ErrInvalidInput, u.ExternalID, t)
			}
		}
	}

	return nil
}

func validateState(s *State) error {
	seen := map[string]bool{}
	for _, ou := range s.OrgUnits {
		if err := addID(seen, "state org unit", ou.ExternalID); err != nil {
			return err
		}
	}

	seen = map[string]bool{}
	for _, t := range s.Teams {
		if err := addID(seen, "state team", t.ExternalID); err != nil {
			return err
		}
	}

	seen = map[string]bool{}
	for _, u := range s.Users {
		if err := addID(seen, "state user", u.ExternalID); err != nil {
			return err
		}
	}

	return nil
}

func addID(seen map[string]bool, what, id string) error {
	if id == "" {
		return fmt.Errorf("%w: %s with empty external ID", ErrInvalidInput, what)
	}

	if seen[id] {
		return fmt.Errorf("%w: duplicate %s %q", ErrInvalidInput, what, id)
	}

	seen[id] = true

	return nil
}

func diffOrgUnits(desired []OrgUnit, current []OrgUnitState) ([]OrgUnitChange, error) {
	stored := make(map[string]OrgUnitState, len(current))
	for _, ou := range current {
		stored[ou.ExternalID] = ou
	}

	ordered, err := parentsFirst(desired)
	if err != nil {
		return nil, err
	}

	var changes []OrgUnitChange

	present := make(map[string]bool, len(desired))

	for _, ou := range ordered {
		present[ou.ExternalID] = true
		change := OrgUnitChange{ExternalID: ou.ExternalID, ParentExternalID: ou.ParentExternalID, DisplayName: ou.DisplayName}

		old, ok := stored[ou.ExternalID]

		switch {
		case !ok:
			change.Action = ActionCreate
		case !old.Active:
			change.Action = ActionReactivate
		case old.DisplayName != ou.DisplayName || old.ParentExternalID != ou.ParentExternalID:
			change.Action = ActionUpdate
		default:
			continue
		}

		changes = append(changes, change)
	}

	gone := make([]OrgUnitState, 0)

	for _, ou := range current {
		if !present[ou.ExternalID] && ou.Active {
			gone = append(gone, ou)
		}
	}

	sort.Slice(gone, func(i, j int) bool { return gone[i].ExternalID < gone[j].ExternalID })

	for _, ou := range gone {
		changes = append(changes, OrgUnitChange{
			Action:           ActionDeactivate,
			ExternalID:       ou.ExternalID,
			ParentExternalID: ou.ParentExternalID,
			DisplayName:      ou.DisplayName,
		})
	}

	return changes, nil
}

// parentsFirst orders org units breadth-first from the roots, siblings by
// external ID. Applying writes in this order never points a node at a parent
// that does not exist yet, and never builds a cycle: when a node is written,
// every ancestor it gets is already in its final place.
func parentsFirst(units []OrgUnit) ([]OrgUnit, error) {
	children := make(map[string][]OrgUnit, len(units))
	for _, ou := range units {
		children[ou.ParentExternalID] = append(children[ou.ParentExternalID], ou)
	}

	for _, list := range children {
		sort.Slice(list, func(i, j int) bool { return list[i].ExternalID < list[j].ExternalID })
	}

	ordered := make([]OrgUnit, 0, len(units))
	queue := []string{""}

	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]

		for _, ou := range children[parent] {
			ordered = append(ordered, ou)
			queue = append(queue, ou.ExternalID)
		}
	}

	if len(ordered) != len(units) {
		return nil, fmt.Errorf("%w: snapshot org units contain a cycle", ErrInvalidInput)
	}

	return ordered, nil
}

func diffTeams(desired []Team, current []TeamState) []TeamChange {
	stored := make(map[string]TeamState, len(current))
	for _, t := range current {
		stored[t.ExternalID] = t
	}

	sorted := slices.Clone(desired)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ExternalID < sorted[j].ExternalID })

	var changes []TeamChange

	present := make(map[string]bool, len(desired))

	for _, t := range sorted {
		present[t.ExternalID] = true
		change := TeamChange{ExternalID: t.ExternalID, DisplayName: t.DisplayName}

		old, ok := stored[t.ExternalID]

		switch {
		case !ok:
			change.Action = ActionCreate
		case !old.Active:
			change.Action = ActionReactivate
		case old.DisplayName != t.DisplayName:
			change.Action = ActionUpdate
		default:
			continue
		}

		changes = append(changes, change)
	}

	gone := make([]TeamState, 0)

	for _, t := range current {
		if !present[t.ExternalID] && t.Active {
			gone = append(gone, t)
		}
	}

	sort.Slice(gone, func(i, j int) bool { return gone[i].ExternalID < gone[j].ExternalID })

	for _, t := range gone {
		changes = append(changes, TeamChange{Action: ActionDeactivate, ExternalID: t.ExternalID, DisplayName: t.DisplayName})
	}

	return changes
}

func diffUsers(desired []User, current []UserState) ([]UserChange, []OrgUnitMembershipChange, []TeamMembershipChange) {
	stored := make(map[string]UserState, len(current))
	for _, u := range current {
		stored[u.ExternalID] = u
	}

	sorted := slices.Clone(desired)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ExternalID < sorted[j].ExternalID })

	var (
		users   []UserChange
		orgs    []OrgUnitMembershipChange
		teams   []TeamMembershipChange
		present = make(map[string]bool, len(desired))
	)

	for _, u := range sorted {
		present[u.ExternalID] = true
		change := UserChange{ExternalID: u.ExternalID, Username: u.Username, Email: u.Email, DisplayName: u.DisplayName}

		old, exists := stored[u.ExternalID]

		switch {
		case !exists && u.Disabled:
			// Never logged in and disabled: there is nothing to create or deactivate.
			continue
		case !exists:
			change.Action = ActionCreate
		case u.Disabled && old.Active:
			change.Action = ActionDeactivate
		case !u.Disabled && !old.Active:
			change.Action = ActionReactivate
		case old.Username != u.Username || old.Email != u.Email || old.DisplayName != u.DisplayName:
			change.Action = ActionUpdate
		}

		if change.Action != "" {
			users = append(users, change)
		}

		if old.OrgUnitExternalID != u.OrgUnitExternalID {
			orgs = append(orgs, OrgUnitMembershipChange{UserExternalID: u.ExternalID, OrgUnitExternalID: u.OrgUnitExternalID})
		}

		teams = append(teams, diffTeamMembers(u.ExternalID, old.TeamExternalIDs, u.TeamExternalIDs)...)
	}

	gone := make([]UserState, 0)

	for _, u := range current {
		if !present[u.ExternalID] {
			gone = append(gone, u)
		}
	}

	sort.Slice(gone, func(i, j int) bool { return gone[i].ExternalID < gone[j].ExternalID })

	for _, u := range gone {
		if u.Active {
			users = append(users, UserChange{
				Action:      ActionDeactivate,
				ExternalID:  u.ExternalID,
				Username:    u.Username,
				Email:       u.Email,
				DisplayName: u.DisplayName,
			})
		}

		if u.OrgUnitExternalID != "" {
			orgs = append(orgs, OrgUnitMembershipChange{UserExternalID: u.ExternalID})
		}

		teams = append(teams, diffTeamMembers(u.ExternalID, u.TeamExternalIDs, nil)...)
	}

	return users, orgs, teams
}

func diffTeamMembers(user string, have, want []string) []TeamMembershipChange {
	haveSet := toSet(have)
	wantSet := toSet(want)

	var changes []TeamMembershipChange

	for _, t := range sortedKeys(wantSet) {
		if !haveSet[t] {
			changes = append(changes, TeamMembershipChange{UserExternalID: user, TeamExternalID: t})
		}
	}

	for _, t := range sortedKeys(haveSet) {
		if !wantSet[t] {
			changes = append(changes, TeamMembershipChange{UserExternalID: user, TeamExternalID: t, Remove: true})
		}
	}

	return changes
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}

	return set
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
