// Package orgsync plans how to bring a stored organization (departments,
// teams, users, memberships) in line with a directory Snapshot. Objects are
// matched by external ID only, since names and emails change; nothing is
// deleted, so a returning object keeps its grants. Standard library only.
package orgsync

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
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

// Diff returns the writes that make state match snapshot. It does no I/O.
// Users are created before their first login, so grants and memberships can
// be given ahead; a disabled user who never logged in is not created at all.
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
	for _, kind := range []struct {
		what string
		ids  []string
	}{
		{"state org unit", externalIDs(s.OrgUnits)},
		{"state team", externalIDs(s.Teams)},
		{"state user", externalIDs(s.Users)},
	} {
		seen := make(map[string]bool, len(kind.ids))
		for _, id := range kind.ids {
			if err := addID(seen, kind.what, id); err != nil {
				return err
			}
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
	ordered, err := parentsFirst(desired)
	if err != nil {
		return nil, err
	}

	var changes []OrgUnitChange

	gone := match(ordered, current, func(ou OrgUnit, old OrgUnitState, exists bool) {
		changed := old.DisplayName != ou.DisplayName || old.ParentExternalID != ou.ParentExternalID
		if a := action(exists, old.Active, changed); a != "" {
			changes = append(changes, OrgUnitChange{Action: a, ExternalID: ou.ExternalID, ParentExternalID: ou.ParentExternalID, DisplayName: ou.DisplayName})
		}
	})

	for _, ou := range gone {
		if ou.Active {
			changes = append(changes, OrgUnitChange{
				Action:           ActionDeactivate,
				ExternalID:       ou.ExternalID,
				ParentExternalID: ou.ParentExternalID,
				DisplayName:      ou.DisplayName,
			})
		}
	}

	return changes, nil
}

// parentsFirst orders org units breadth-first from the roots, siblings by
// external ID. Applying writes in this order never points a node at a parent
// that does not exist yet, and never builds a cycle: when a node is written,
// every ancestor it gets is already in its final place.
func parentsFirst(units []OrgUnit) ([]OrgUnit, error) {
	children := make(map[string][]OrgUnit, len(units))
	for _, ou := range byExternalID(units) {
		children[ou.ParentExternalID] = append(children[ou.ParentExternalID], ou)
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
		return nil, cycleError(units, ordered)
	}

	return ordered, nil
}

// cycleError names the units of one parent cycle. Every unit not reached
// from a root is in a cycle or below one, and its parents all exist (see
// validateSnapshot), so following them from such a unit ends in a cycle.
func cycleError(units, reached []OrgUnit) error {
	parent := make(map[string]string, len(units))
	for _, ou := range units {
		parent[ou.ExternalID] = ou.ParentExternalID
	}

	done := toSet(externalIDs(reached))
	start := ""

	for _, ou := range byExternalID(units) {
		if !done[ou.ExternalID] {
			start = ou.ExternalID
			break
		}
	}

	visited := map[string]bool{}
	for !visited[start] {
		visited[start] = true
		start = parent[start]
	}

	cycle := []string{start}
	for id := parent[start]; id != start; id = parent[id] {
		cycle = append(cycle, id)
	}

	slices.Sort(cycle)

	return fmt.Errorf("%w: snapshot org units form a parent cycle: %s", ErrInvalidInput, strings.Join(cycle, ", "))
}

func diffTeams(desired []Team, current []TeamState) []TeamChange {
	var changes []TeamChange

	gone := match(byExternalID(desired), current, func(t Team, old TeamState, exists bool) {
		if a := action(exists, old.Active, old.DisplayName != t.DisplayName); a != "" {
			changes = append(changes, TeamChange{Action: a, ExternalID: t.ExternalID, DisplayName: t.DisplayName})
		}
	})

	for _, t := range gone {
		if t.Active {
			changes = append(changes, TeamChange{Action: ActionDeactivate, ExternalID: t.ExternalID, DisplayName: t.DisplayName})
		}
	}

	return changes
}

func diffUsers(desired []User, current []UserState) ([]UserChange, []OrgUnitMembershipChange, []TeamMembershipChange) {
	var (
		users []UserChange
		orgs  []OrgUnitMembershipChange
		teams []TeamMembershipChange
	)

	gone := match(byExternalID(desired), current, func(u User, old UserState, exists bool) {
		change := UserChange{ExternalID: u.ExternalID, Username: u.Username, Email: u.Email, DisplayName: u.DisplayName}

		switch {
		case !exists && u.Disabled:
			// Never logged in and disabled: there is nothing to create or deactivate.
			return
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
	})

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

// action is the write for a department or team that is in the snapshot; empty
// when there is none.
func action(exists, active, changed bool) Action {
	switch {
	case !exists:
		return ActionCreate
	case !active:
		return ActionReactivate
	case changed:
		return ActionUpdate
	default:
		return ""
	}
}

// identified is an object matched by its external ID.
type identified interface {
	id() string
}

func (o OrgUnit) id() string      { return o.ExternalID }
func (o OrgUnitState) id() string { return o.ExternalID }
func (t Team) id() string         { return t.ExternalID }
func (t TeamState) id() string    { return t.ExternalID }
func (u User) id() string         { return u.ExternalID }
func (u UserState) id() string    { return u.ExternalID }

func externalIDs[T identified](items []T) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.id()
	}

	return ids
}

// byExternalID returns a copy of items sorted by external ID.
func byExternalID[T identified](items []T) []T {
	return slices.SortedFunc(slices.Values(items), func(a, b T) int { return strings.Compare(a.id(), b.id()) })
}

// match calls visit for each desired object, in order, with the stored object
// of the same external ID (exists is false when there is none). It returns the
// stored objects missing from desired, by external ID.
func match[D, S identified](desired []D, current []S, visit func(d D, old S, exists bool)) []S {
	stored := make(map[string]S, len(current))
	for _, s := range current {
		stored[s.id()] = s
	}

	for _, d := range desired {
		id := d.id()
		old, exists := stored[id]
		visit(d, old, exists)
		delete(stored, id)
	}

	return byExternalID(slices.Collect(maps.Values(stored)))
}

// diffTeamMembers lists the team memberships to add, then to remove, each by
// team ID.
func diffTeamMembers(user string, have, want []string) []TeamMembershipChange {
	if slices.Equal(have, want) {
		return nil
	}

	haveSet := toSet(have)
	wantSet := toSet(want)

	var changes []TeamMembershipChange

	for _, t := range slices.Sorted(maps.Keys(wantSet)) {
		if !haveSet[t] {
			changes = append(changes, TeamMembershipChange{UserExternalID: user, TeamExternalID: t})
		}
	}

	for _, t := range slices.Sorted(maps.Keys(haveSet)) {
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
