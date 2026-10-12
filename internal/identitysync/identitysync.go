// Package identitysync applies the organization sync of an identity source:
// it reads the directory, loads what neutree stores for the source, asks
// orgsync.Diff for the writes that bring the two in line, and applies them.
//
// Departments become OrgUnits, groups become Teams, and every directory user
// gets a GoTrue user, a link in api.external_identities and a profile, whether
// or not it has logged in (a later login finds the user by its link). A user
// that is disabled or gone in the directory is banned in GoTrue, never
// deleted; a user the sync banned is unbanned when the directory enables it
// again. A user banned for any other reason is left banned.
//
// A sync either fails before its first write (the directory or the stored
// state could not be read, or they are inconsistent) or applies its writes in
// order until one fails. It does not roll back: every write is idempotent
// with respect to the directory, so the next sync continues from wherever the
// last one stopped.
package identitysync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// BanDuration is how long a deactivated user is banned in GoTrue: in effect
// for ever, until a sync reactivates the user.
const BanDuration = 100 * 365 * 24 * time.Hour

// ErrReadDirectory wraps a failure to read the directory. Nothing was
// written.
var ErrReadDirectory = errors.New("read directory")

// ErrLoadState wraps a failure to read what neutree stores for the source,
// or to compare it with the directory. Nothing was written.
var ErrLoadState = errors.New("load stored state")

// Directory reads a whole directory; *ldap.Directory implements it.
type Directory interface {
	Snapshot(ctx context.Context) (*orgsync.Snapshot, error)
}

// Store is the storage the sync reads and writes; storage.Storage
// implements it.
type Store interface {
	ListOrgUnit(option storage.ListOption) ([]v1.OrgUnit, error)
	CreateOrgUnit(data *v1.OrgUnit) error
	UpdateOrgUnit(id string, data *v1.OrgUnit) error
	SetOrgUnitMember(data *storage.OrgUnitMember) error
	DeleteOrgUnitMember(userID string) error

	ListTeam(option storage.ListOption) ([]v1.Team, error)
	CreateTeam(data *v1.Team) error
	UpdateTeam(id string, data *v1.Team) error
	AddTeamMember(data *storage.TeamMember) error
	DeleteTeamMember(teamID int, userID string) error

	GetUserProfile(id string) (*v1.UserProfile, error)
	UpdateUserProfile(id string, data *v1.UserProfile) error

	GetExternalIdentity(source, externalID string) (*storage.ExternalIdentity, error)
	CreateExternalIdentity(data *storage.ExternalIdentity) error
	UpdateExternalIdentitySync(data *storage.ExternalIdentity) error
	ListIdentitySourceSyncUsers(identitySource, linkSource string) ([]storage.IdentitySourceSyncUser, error)
}

// Syncer syncs identity sources into a Store and GoTrue.
type Syncer struct {
	Store Store
	Auth  auth.Client
	// Now defaults to time.Now.
	Now func() time.Time
}

// Result counts the writes of one sync. Created, Updated, Reactivated and
// Deactivated cover OrgUnits, Teams and users together.
type Result struct {
	Planned     int
	Created     int
	Updated     int
	Reactivated int
	Deactivated int
	Memberships int
}

// Applied is the number of writes that were applied.
func (r Result) Applied() int {
	return r.Created + r.Updated + r.Reactivated + r.Deactivated + r.Memberships
}

// Pending is the number of planned writes that were not applied.
func (r Result) Pending() int {
	return r.Planned - r.Applied()
}

// ObjectName is the metadata.name of the OrgUnit or Team synced from the
// directory object externalID of the identity source sourceName. It is stable
// (the external ID never changes) and at most 45 characters: a source name has
// at most 32.
func ObjectName(sourceName, externalID string) string {
	sum := sha256.Sum256([]byte(externalID))
	return sourceName + "-" + hex.EncodeToString(sum[:])[:12]
}

// Run syncs the LDAP identity source sourceName from dir.
func (s *Syncer) Run(ctx context.Context, sourceName string, dir Directory) (Result, error) {
	snapshot, err := dir.Snapshot(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrReadDirectory, err)
	}

	normalizeSnapshot(snapshot)

	run := &run{
		Syncer:     s,
		ctx:        ctx,
		source:     sourceName,
		linkSource: auth.LinkSource(auth.LDAPSource, sourceName),
	}

	state, err := run.loadState()
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrLoadState, err)
	}

	plan, err := orgsync.Diff(snapshot, state)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %w", ErrLoadState, err)
	}

	run.result.Planned = len(plan.OrgUnits) + len(plan.Teams) + len(plan.Users) +
		len(plan.OrgUnitMemberships) + len(plan.TeamMemberships)

	err = run.apply(plan)

	return run.result, err
}

// normalizeSnapshot fills in what neutree would derive anyway, so a value
// the directory leaves empty does not count as a change on every sync: a user
// profile without a display name shows the username.
func normalizeSnapshot(snapshot *orgsync.Snapshot) {
	for i := range snapshot.Users {
		if snapshot.Users[i].DisplayName == "" {
			snapshot.Users[i].DisplayName = snapshot.Users[i].Username
		}
	}
}

type run struct {
	*Syncer

	ctx        context.Context
	source     string
	linkSource string
	result     Result

	orgUnits map[string]*v1.OrgUnit // by external ID
	teams    map[string]*v1.Team    // by external ID
	users    map[string]storage.IdentitySourceSyncUser
}

func (r *run) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}

	return time.Now()
}

func (r *run) loadState() (*orgsync.State, error) {
	if err := r.loadOrgUnits(); err != nil {
		return nil, err
	}

	if err := r.loadTeams(); err != nil {
		return nil, err
	}

	users, err := r.Store.ListIdentitySourceSyncUsers(r.source, r.linkSource)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}

	state := &orgsync.State{}

	nameToExternalID := make(map[string]string, len(r.orgUnits))
	for id, ou := range r.orgUnits {
		nameToExternalID[ou.Metadata.Name] = id
	}

	for id, ou := range r.orgUnits {
		parent := ""
		if ou.Spec.Parent != "" {
			// A parent outside this source cannot exist (the database
			// refuses it); an unknown one reads as a root, which Diff
			// then corrects.
			parent = nameToExternalID[ou.Spec.Parent]
		}

		state.OrgUnits = append(state.OrgUnits, orgsync.OrgUnitState{
			ExternalID:       id,
			ParentExternalID: parent,
			DisplayName:      ou.Metadata.DisplayName,
			Active:           ou.IsActive(),
		})
	}

	for id, t := range r.teams {
		state.Teams = append(state.Teams, orgsync.TeamState{
			ExternalID:  id,
			DisplayName: t.Metadata.DisplayName,
			Active:      t.IsActive(),
		})
	}

	r.users = make(map[string]storage.IdentitySourceSyncUser, len(users))

	for _, u := range users {
		r.users[u.ExternalID] = u
		state.Users = append(state.Users, orgsync.UserState{
			ExternalID:  u.ExternalID,
			Username:    u.Username,
			Email:       u.Email,
			DisplayName: u.DisplayName,
			// Only a deactivation by the sync makes a user inactive here: a
			// user banned for another reason is not the sync's to lift.
			Active:            u.SyncDeactivated == "",
			OrgUnitExternalID: u.OrgUnitExternalID,
			TeamExternalIDs:   u.TeamExternalIDs,
		})
	}

	return state, nil
}

func (r *run) sourceFilter() storage.ListOption {
	return storage.ListOption{Filters: []storage.Filter{
		{Column: "spec->>identity_source", Operator: "eq", Value: r.source},
	}}
}

func (r *run) loadOrgUnits() error {
	list, err := r.Store.ListOrgUnit(r.sourceFilter())
	if err != nil {
		return fmt.Errorf("list org units: %w", err)
	}

	r.orgUnits = make(map[string]*v1.OrgUnit, len(list))

	for i := range list {
		ou := &list[i]
		if ou.Metadata == nil || ou.Spec == nil || ou.Spec.ExternalID == "" {
			return fmt.Errorf("org unit %d has no metadata or external ID", ou.ID)
		}

		r.orgUnits[ou.Spec.ExternalID] = ou
	}

	return nil
}

func (r *run) loadTeams() error {
	list, err := r.Store.ListTeam(r.sourceFilter())
	if err != nil {
		return fmt.Errorf("list teams: %w", err)
	}

	r.teams = make(map[string]*v1.Team, len(list))

	for i := range list {
		t := &list[i]
		if t.Metadata == nil || t.Spec == nil || t.Spec.ExternalID == "" {
			return fmt.Errorf("team %d has no metadata or external ID", t.ID)
		}

		r.teams[t.Spec.ExternalID] = t
	}

	return nil
}

func (r *run) apply(plan *orgsync.Plan) error {
	created := false

	for _, change := range plan.OrgUnits {
		if err := r.checkContext(); err != nil {
			return err
		}

		if err := r.applyOrgUnit(change); err != nil {
			return fmt.Errorf("%s org unit %s: %w", change.Action, change.ExternalID, err)
		}

		created = created || change.Action == orgsync.ActionCreate
	}

	// Memberships need the database IDs of the new OrgUnits.
	if created && len(plan.OrgUnitMemberships) > 0 {
		if err := r.loadOrgUnits(); err != nil {
			return err
		}
	}

	created = false

	for _, change := range plan.Teams {
		if err := r.checkContext(); err != nil {
			return err
		}

		if err := r.applyTeam(change); err != nil {
			return fmt.Errorf("%s team %s: %w", change.Action, change.ExternalID, err)
		}

		created = created || change.Action == orgsync.ActionCreate
	}

	if created && len(plan.TeamMemberships) > 0 {
		if err := r.loadTeams(); err != nil {
			return err
		}
	}

	for _, change := range plan.Users {
		if err := r.checkContext(); err != nil {
			return err
		}

		if err := r.applyUser(change); err != nil {
			return fmt.Errorf("%s user %s (%s): %w", change.Action, change.Username, change.ExternalID, err)
		}
	}

	for _, change := range plan.OrgUnitMemberships {
		if err := r.checkContext(); err != nil {
			return err
		}

		if err := r.applyOrgUnitMembership(change); err != nil {
			return fmt.Errorf("set org unit of user %s: %w", change.UserExternalID, err)
		}

		r.result.Memberships++
	}

	for _, change := range plan.TeamMemberships {
		if err := r.checkContext(); err != nil {
			return err
		}

		if err := r.applyTeamMembership(change); err != nil {
			return fmt.Errorf("change team %s of user %s: %w", change.TeamExternalID, change.UserExternalID, err)
		}

		r.result.Memberships++
	}

	return nil
}

func (r *run) checkContext() error {
	if err := r.ctx.Err(); err != nil {
		return fmt.Errorf("sync stopped: %w", err)
	}

	return nil
}

func (r *run) count(action orgsync.Action) {
	switch action {
	case orgsync.ActionCreate:
		r.result.Created++
	case orgsync.ActionUpdate:
		r.result.Updated++
	case orgsync.ActionReactivate:
		r.result.Reactivated++
	case orgsync.ActionDeactivate:
		r.result.Deactivated++
	}
}

func (r *run) orgUnitName(externalID string) (string, error) {
	if externalID == "" {
		return "", nil
	}

	ou, ok := r.orgUnits[externalID]
	if !ok {
		return "", fmt.Errorf("parent %s is not stored", externalID)
	}

	return ou.Metadata.Name, nil
}

func (r *run) applyOrgUnit(change orgsync.OrgUnitChange) error {
	if change.Action == orgsync.ActionDeactivate {
		stored, ok := r.orgUnits[change.ExternalID]
		if !ok {
			return errors.New("not stored")
		}

		if err := r.Store.UpdateOrgUnit(strconv.Itoa(stored.ID), &v1.OrgUnit{
			Status: &v1.OrgUnitStatus{Phase: v1.OrgPhaseInactive},
		}); err != nil {
			return err
		}

		r.count(change.Action)

		return nil
	}

	parent, err := r.orgUnitName(change.ParentExternalID)
	if err != nil {
		return err
	}

	spec := &v1.OrgUnitSpec{IdentitySource: r.source, ExternalID: change.ExternalID, Parent: parent}

	if change.Action == orgsync.ActionCreate {
		ou := &v1.OrgUnit{
			APIVersion: "v1",
			Kind:       "OrgUnit",
			Metadata:   &v1.Metadata{Name: ObjectName(r.source, change.ExternalID), DisplayName: change.DisplayName},
			Spec:       spec,
			Status:     &v1.OrgUnitStatus{Phase: v1.OrgPhaseActive},
		}

		if err := r.Store.CreateOrgUnit(ou); err != nil {
			return err
		}

		// The ID is read back before memberships are written; children only
		// need the name.
		r.orgUnits[change.ExternalID] = ou
		r.count(change.Action)

		return nil
	}

	stored, ok := r.orgUnits[change.ExternalID]
	if !ok {
		return errors.New("not stored")
	}

	// metadata and spec are composites PostgREST replaces whole: send the
	// stored metadata with the new display name, and all of spec.
	metadata := *stored.Metadata
	metadata.DisplayName = change.DisplayName
	update := &v1.OrgUnit{Metadata: &metadata, Spec: spec}

	if change.Action == orgsync.ActionReactivate {
		update.Status = &v1.OrgUnitStatus{Phase: v1.OrgPhaseActive}
	}

	if err := r.Store.UpdateOrgUnit(strconv.Itoa(stored.ID), update); err != nil {
		return err
	}

	stored.Metadata = &metadata
	stored.Spec = spec

	r.count(change.Action)

	return nil
}

func (r *run) applyTeam(change orgsync.TeamChange) error {
	spec := &v1.TeamSpec{IdentitySource: r.source, ExternalID: change.ExternalID}

	if change.Action == orgsync.ActionCreate {
		t := &v1.Team{
			APIVersion: "v1",
			Kind:       "Team",
			Metadata:   &v1.Metadata{Name: ObjectName(r.source, change.ExternalID), DisplayName: change.DisplayName},
			Spec:       spec,
			Status:     &v1.TeamStatus{Phase: v1.OrgPhaseActive},
		}

		if err := r.Store.CreateTeam(t); err != nil {
			return err
		}

		r.teams[change.ExternalID] = t
		r.count(change.Action)

		return nil
	}

	stored, ok := r.teams[change.ExternalID]
	if !ok {
		return errors.New("not stored")
	}

	var update *v1.Team

	switch change.Action {
	case orgsync.ActionDeactivate:
		update = &v1.Team{Status: &v1.TeamStatus{Phase: v1.OrgPhaseInactive}}
	default:
		metadata := *stored.Metadata
		metadata.DisplayName = change.DisplayName
		update = &v1.Team{Metadata: &metadata, Spec: spec}

		if change.Action == orgsync.ActionReactivate {
			update.Status = &v1.TeamStatus{Phase: v1.OrgPhaseActive}
		}
	}

	if err := r.Store.UpdateTeam(strconv.Itoa(stored.ID), update); err != nil {
		return err
	}

	r.count(change.Action)

	return nil
}

func (r *run) applyUser(change orgsync.UserChange) error {
	switch change.Action {
	case orgsync.ActionCreate:
		return r.createUser(change)
	case orgsync.ActionUpdate:
		stored := r.users[change.ExternalID]
		if err := r.updateUser(stored.UserID, change, stored.SyncDeactivated); err != nil {
			return err
		}
	case orgsync.ActionDeactivate:
		if err := r.deactivateUser(change); err != nil {
			return err
		}
	case orgsync.ActionReactivate:
		if err := r.reactivateUser(change); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown action %q", change.Action)
	}

	r.count(change.Action)

	return nil
}

func (r *run) createUser(change orgsync.UserChange) error {
	users := &auth.ExternalUsers{Client: r.Auth, Links: r.Store}

	userID, created, err := users.Ensure(r.ctx,
		auth.LDAPAccount(r.source, change.ExternalID, change.Username, change.DisplayName, change.Email))
	if err != nil {
		return err
	}

	r.users[change.ExternalID] = storage.IdentitySourceSyncUser{
		ExternalID:  change.ExternalID,
		UserID:      userID,
		Username:    change.Username,
		Email:       change.Email,
		DisplayName: change.DisplayName,
	}

	if created {
		r.count(change.Action)
		return nil
	}

	// A login created the user since the state was read: bring it in line
	// like any stored user.
	if err := r.updateUser(userID, change, ""); err != nil {
		return err
	}

	r.count(orgsync.ActionUpdate)

	return nil
}

// updateUser writes the directory's username, display name and email to the
// GoTrue user, its profile and its link. marker is the link's
// sync_deactivated value to keep.
func (r *run) updateUser(userID string, change orgsync.UserChange, marker string) error {
	id, err := uuid.Parse(userID)
	if err != nil {
		return fmt.Errorf("invalid user ID %q: %w", userID, err)
	}

	metadata := auth.ExternalUserMetadata(change.Username, change.DisplayName, change.Email)
	// A key set to null is removed from user_metadata.
	for _, key := range []string{"preferred_username", "name", "email"} {
		if _, ok := metadata[key]; !ok {
			metadata[key] = nil
		}
	}

	updated, err := r.Auth.AdminUpdateUser(types.AdminUpdateUserRequest{UserID: id, UserMetadata: metadata})
	if err != nil {
		return fmt.Errorf("update GoTrue user: %w", err)
	}

	gotrueEmail := ""
	if updated != nil {
		gotrueEmail = updated.Email
	}

	if err := r.updateProfile(userID, change, gotrueEmail); err != nil {
		return err
	}

	if err := r.Store.UpdateExternalIdentitySync(&storage.ExternalIdentity{
		Source:          r.linkSource,
		ExternalID:      change.ExternalID,
		Username:        change.Username,
		Email:           change.Email,
		SyncDeactivated: marker,
	}); err != nil {
		return fmt.Errorf("update link: %w", err)
	}

	return nil
}

// updateProfile sets the profile's display name, and its email to what the
// database derives for an external user: the directory email, or the GoTrue
// (placeholder) email when the directory has none.
func (r *run) updateProfile(userID string, change orgsync.UserChange, gotrueEmail string) error {
	profile, err := r.Store.GetUserProfile(userID)
	if err != nil {
		return fmt.Errorf("get profile: %w", err)
	}

	if profile.Metadata == nil {
		return errors.New("profile has no metadata")
	}

	email := change.Email
	if email == "" {
		email = gotrueEmail
	}

	if profile.Metadata.DisplayName == change.DisplayName && profile.Spec != nil && profile.Spec.Email == email {
		return nil
	}

	metadata := *profile.Metadata
	metadata.DisplayName = change.DisplayName

	if err := r.Store.UpdateUserProfile(userID, &v1.UserProfile{
		Metadata: &metadata,
		Spec:     &v1.UserProfileSpec{Email: email},
	}); err != nil {
		return fmt.Errorf("update profile: %w", err)
	}

	return nil
}

// deactivateUser bans the GoTrue user, which blocks logins and token
// refreshes, and marks the link as deactivated by the sync. A user that is
// already banned is left as it is and marked so that a reactivation does not
// lift that ban.
func (r *run) deactivateUser(change orgsync.UserChange) error {
	stored, ok := r.users[change.ExternalID]
	if !ok {
		return errors.New("not stored")
	}

	id, err := uuid.Parse(stored.UserID)
	if err != nil {
		return fmt.Errorf("invalid user ID %q: %w", stored.UserID, err)
	}

	user, err := r.Auth.AdminGetUser(types.AdminGetUserRequest{UserID: id})
	if err != nil {
		return fmt.Errorf("get GoTrue user: %w", err)
	}

	marker := storage.SyncDeactivatedBanned

	if auth.IsBanned(&user.User, r.now()) {
		marker = storage.SyncDeactivatedKept
	} else {
		ban := types.BanDurationTime(BanDuration)
		if _, err := r.Auth.AdminUpdateUser(types.AdminUpdateUserRequest{UserID: id, BanDuration: &ban}); err != nil {
			return fmt.Errorf("ban GoTrue user: %w", err)
		}
	}

	if err := r.Store.UpdateExternalIdentitySync(&storage.ExternalIdentity{
		Source:          r.linkSource,
		ExternalID:      change.ExternalID,
		Username:        stored.Username,
		Email:           stored.Email,
		SyncDeactivated: marker,
	}); err != nil {
		return fmt.Errorf("update link: %w", err)
	}

	return nil
}

// reactivateUser lifts the ban the sync set (none when the user was banned
// for another reason), then writes the directory's current values.
func (r *run) reactivateUser(change orgsync.UserChange) error {
	stored, ok := r.users[change.ExternalID]
	if !ok {
		return errors.New("not stored")
	}

	if stored.SyncDeactivated == storage.SyncDeactivatedBanned {
		id, err := uuid.Parse(stored.UserID)
		if err != nil {
			return fmt.Errorf("invalid user ID %q: %w", stored.UserID, err)
		}

		none := types.BanDurationNone()
		if _, err := r.Auth.AdminUpdateUser(types.AdminUpdateUserRequest{UserID: id, BanDuration: &none}); err != nil {
			return fmt.Errorf("unban GoTrue user: %w", err)
		}
	}

	return r.updateUser(stored.UserID, change, "")
}

func (r *run) userID(externalID string) (string, error) {
	u, ok := r.users[externalID]
	if !ok || u.UserID == "" {
		return "", fmt.Errorf("user %s is not stored", externalID)
	}

	return u.UserID, nil
}

func (r *run) applyOrgUnitMembership(change orgsync.OrgUnitMembershipChange) error {
	userID, err := r.userID(change.UserExternalID)
	if err != nil {
		return err
	}

	if change.OrgUnitExternalID == "" {
		return r.Store.DeleteOrgUnitMember(userID)
	}

	ou, ok := r.orgUnits[change.OrgUnitExternalID]
	if !ok || ou.ID == 0 {
		return fmt.Errorf("org unit %s is not stored", change.OrgUnitExternalID)
	}

	return r.Store.SetOrgUnitMember(&storage.OrgUnitMember{UserID: userID, OrgUnitID: ou.ID, IdentitySource: r.source})
}

func (r *run) applyTeamMembership(change orgsync.TeamMembershipChange) error {
	userID, err := r.userID(change.UserExternalID)
	if err != nil {
		return err
	}

	t, ok := r.teams[change.TeamExternalID]
	if !ok || t.ID == 0 {
		return fmt.Errorf("team %s is not stored", change.TeamExternalID)
	}

	if change.Remove {
		return r.Store.DeleteTeamMember(t.ID, userID)
	}

	return r.Store.AddTeamMember(&storage.TeamMember{TeamID: t.ID, UserID: userID})
}
