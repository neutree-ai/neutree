package identitysync

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const testSource = "corp-ldap"

var testLinkSource = auth.LinkSource(auth.LDAPSource, testSource)

// fakeStore is an in-memory Store that behaves like the database for what
// the sync uses: OrgUnits and Teams keyed by id, members, links and profiles.
type fakeStore struct {
	orgUnits    map[int]*v1.OrgUnit
	teams       map[int]*v1.Team
	ouMembers   map[string]storage.OrgUnitMember
	teamMembers map[[2]string]bool
	links       map[string]*storage.ExternalIdentity // by external ID
	profiles    map[string]*v1.UserProfile
	nextID      int

	writes int
	// failAt makes the write with this 1-based number fail; 0 never fails.
	failAt int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		orgUnits:    map[int]*v1.OrgUnit{},
		teams:       map[int]*v1.Team{},
		ouMembers:   map[string]storage.OrgUnitMember{},
		teamMembers: map[[2]string]bool{},
		links:       map[string]*storage.ExternalIdentity{},
		profiles:    map[string]*v1.UserProfile{},
		nextID:      1,
	}
}

var errInjected = errors.New("injected write failure")

func (s *fakeStore) write() error {
	s.writes++
	if s.failAt != 0 && s.writes == s.failAt {
		return errInjected
	}

	return nil
}

func (s *fakeStore) ListOrgUnit(option storage.ListOption) ([]v1.OrgUnit, error) {
	var out []v1.OrgUnit

	for _, ou := range s.orgUnits {
		if ou.Spec.IdentitySource == option.Filters[0].Value {
			out = append(out, *ou)
		}
	}

	return out, nil
}

func (s *fakeStore) CreateOrgUnit(data *v1.OrgUnit) error {
	if err := s.write(); err != nil {
		return err
	}

	copied := *data
	metadata := *data.Metadata
	spec := *data.Spec
	copied.ID, copied.Metadata, copied.Spec = s.nextID, &metadata, &spec
	s.orgUnits[s.nextID] = &copied
	s.nextID++

	return nil
}

func (s *fakeStore) UpdateOrgUnit(id string, data *v1.OrgUnit) error {
	if err := s.write(); err != nil {
		return err
	}

	n, _ := strconv.Atoi(id)
	ou := s.orgUnits[n]

	if data.Metadata != nil {
		metadata := *data.Metadata
		ou.Metadata = &metadata
	}

	if data.Spec != nil {
		spec := *data.Spec
		ou.Spec = &spec
	}

	if data.Status != nil {
		status := *data.Status
		ou.Status = &status
	}

	return nil
}

func (s *fakeStore) SetOrgUnitMember(data *storage.OrgUnitMember) error {
	if err := s.write(); err != nil {
		return err
	}

	s.ouMembers[data.UserID] = *data

	return nil
}

func (s *fakeStore) DeleteOrgUnitMember(userID string) error {
	if err := s.write(); err != nil {
		return err
	}

	delete(s.ouMembers, userID)

	return nil
}

func (s *fakeStore) ListTeam(option storage.ListOption) ([]v1.Team, error) {
	var out []v1.Team

	for _, t := range s.teams {
		if t.Spec.IdentitySource == option.Filters[0].Value {
			out = append(out, *t)
		}
	}

	return out, nil
}

func (s *fakeStore) CreateTeam(data *v1.Team) error {
	if err := s.write(); err != nil {
		return err
	}

	copied := *data
	metadata := *data.Metadata
	copied.ID, copied.Metadata = s.nextID, &metadata
	s.teams[s.nextID] = &copied
	s.nextID++

	return nil
}

func (s *fakeStore) UpdateTeam(id string, data *v1.Team) error {
	if err := s.write(); err != nil {
		return err
	}

	n, _ := strconv.Atoi(id)
	t := s.teams[n]

	if data.Metadata != nil {
		metadata := *data.Metadata
		t.Metadata = &metadata
	}

	if data.Spec != nil {
		t.Spec = data.Spec
	}

	if data.Status != nil {
		t.Status = data.Status
	}

	return nil
}

func (s *fakeStore) AddTeamMember(data *storage.TeamMember) error {
	if err := s.write(); err != nil {
		return err
	}

	s.teamMembers[[2]string{strconv.Itoa(data.TeamID), data.UserID}] = true

	return nil
}

func (s *fakeStore) DeleteTeamMember(teamID int, userID string) error {
	if err := s.write(); err != nil {
		return err
	}

	delete(s.teamMembers, [2]string{strconv.Itoa(teamID), userID})

	return nil
}

func (s *fakeStore) GetUserProfile(id string) (*v1.UserProfile, error) {
	p, ok := s.profiles[id]
	if !ok {
		return nil, storage.ErrResourceNotFound
	}

	copied := *p

	return &copied, nil
}

func (s *fakeStore) UpdateUserProfile(id string, data *v1.UserProfile) error {
	if err := s.write(); err != nil {
		return err
	}

	p := s.profiles[id]
	p.Metadata = data.Metadata
	p.Spec = data.Spec

	return nil
}

func (s *fakeStore) GetExternalIdentity(source, externalID string) (*storage.ExternalIdentity, error) {
	link, ok := s.links[externalID]
	if !ok || link.Source != source {
		return nil, storage.ErrResourceNotFound
	}

	copied := *link

	return &copied, nil
}

func (s *fakeStore) CreateExternalIdentity(data *storage.ExternalIdentity) error {
	if err := s.write(); err != nil {
		return err
	}

	copied := *data
	s.links[data.ExternalID] = &copied

	return nil
}

func (s *fakeStore) UpdateExternalIdentitySync(data *storage.ExternalIdentity) error {
	if err := s.write(); err != nil {
		return err
	}

	link := s.links[data.ExternalID]
	link.Username, link.Email, link.SyncDeactivated = data.Username, data.Email, data.SyncDeactivated

	return nil
}

func (s *fakeStore) ListIdentitySourceSyncUsers(identitySource, linkSource string) ([]storage.IdentitySourceSyncUser, error) {
	var out []storage.IdentitySourceSyncUser

	for _, link := range s.links {
		if link.Source != linkSource {
			continue
		}

		u := storage.IdentitySourceSyncUser{
			ExternalID:      link.ExternalID,
			UserID:          link.UserID,
			Username:        link.Username,
			Email:           link.Email,
			SyncDeactivated: link.SyncDeactivated,
			TeamExternalIDs: []string{},
		}

		if p, ok := s.profiles[link.UserID]; ok {
			u.DisplayName = p.Metadata.DisplayName
		}

		if m, ok := s.ouMembers[link.UserID]; ok && m.IdentitySource == identitySource {
			u.OrgUnitExternalID = s.orgUnits[m.OrgUnitID].Spec.ExternalID
		}

		for key := range s.teamMembers {
			if key[1] != link.UserID {
				continue
			}

			id, _ := strconv.Atoi(key[0])
			if t := s.teams[id]; t.Spec.IdentitySource == identitySource {
				u.TeamExternalIDs = append(u.TeamExternalIDs, t.Spec.ExternalID)
			}
		}

		sort.Strings(u.TeamExternalIDs)
		out = append(out, u)
	}

	return out, nil
}

// fakeGoTrue is an in-memory GoTrue admin API. Creating a user creates its
// profile in store, like api.handle_new_user.
type fakeGoTrue struct {
	store *fakeStore
	users map[uuid.UUID]*types.User

	creates, bans, unbans int
}

func newFakeGoTrue(store *fakeStore) *fakeGoTrue {
	return &fakeGoTrue{store: store, users: map[uuid.UUID]*types.User{}}
}

func (g *fakeGoTrue) AdminGetUser(req types.AdminGetUserRequest) (*types.AdminGetUserResponse, error) {
	u, ok := g.users[req.UserID]
	if !ok {
		return nil, errors.New("user not found")
	}

	return &types.AdminGetUserResponse{User: *u}, nil
}

func (g *fakeGoTrue) AdminCreateUser(req types.AdminCreateUserRequest) (*types.AdminCreateUserResponse, error) {
	for _, u := range g.users {
		if u.Email == req.Email {
			return nil, errors.New("email exists")
		}
	}

	g.creates++
	u := &types.User{ID: uuid.New(), Email: req.Email, UserMetadata: req.UserMetadata, AppMetadata: req.AppMetadata}
	g.users[u.ID] = u

	display, _ := req.UserMetadata["name"].(string)
	email, _ := req.UserMetadata["email"].(string)

	if email == "" {
		email = req.Email
	}

	username, _ := req.UserMetadata["preferred_username"].(string)
	g.store.profiles[u.ID.String()] = &v1.UserProfile{
		ID:       u.ID.String(),
		Metadata: &v1.Metadata{Name: username, DisplayName: display, CreationTimestamp: "2026-01-01T00:00:00Z"},
		Spec:     &v1.UserProfileSpec{Email: email},
	}

	return &types.AdminCreateUserResponse{User: *u}, nil
}

func (g *fakeGoTrue) AdminUpdateUser(req types.AdminUpdateUserRequest) (*types.AdminUpdateUserResponse, error) {
	u, ok := g.users[req.UserID]
	if !ok {
		return nil, errors.New("user not found")
	}

	for k, v := range req.UserMetadata {
		if v == nil {
			delete(u.UserMetadata, k)
		} else {
			u.UserMetadata[k] = v
		}
	}

	if req.BanDuration != nil {
		if d := req.BanDuration.Value(); d != nil {
			g.bans++
			until := time.Now().Add(*d)
			u.BannedUntil = &until
		} else {
			g.unbans++
			u.BannedUntil = nil
		}
	}

	return &types.AdminUpdateUserResponse{User: *u}, nil
}

func (g *fakeGoTrue) AdminDeleteUser(req types.AdminDeleteUserRequest) error {
	delete(g.users, req.UserID)
	return nil
}

type fakeDirectory struct {
	snapshot *orgsync.Snapshot
	err      error
}

func (d *fakeDirectory) Snapshot(context.Context) (*orgsync.Snapshot, error) {
	if d.err != nil {
		return nil, d.err
	}

	// Run normalizes the snapshot in place; hand out a copy.
	copied := *d.snapshot
	copied.Users = append([]orgsync.User(nil), d.snapshot.Users...)

	return &copied, nil
}

// testSnapshot is rnd > ml > infer and market, teams core and all (core
// nested in all), users lin (infer, core), chen (market), wang (rnd, no
// display name) and off (disabled).
func testSnapshot() *orgsync.Snapshot {
	return &orgsync.Snapshot{
		OrgUnits: []orgsync.OrgUnit{
			{ExternalID: "ou-infer", ParentExternalID: "ou-ml", DisplayName: "infer"},
			{ExternalID: "ou-ml", ParentExternalID: "ou-rnd", DisplayName: "ml"},
			{ExternalID: "ou-rnd", DisplayName: "rnd"},
			{ExternalID: "ou-market", DisplayName: "market"},
		},
		Teams: []orgsync.Team{
			{ExternalID: "g-core", DisplayName: "core"},
			{ExternalID: "g-all", DisplayName: "all"},
		},
		Users: []orgsync.User{
			{ExternalID: "u-lin", Username: "lin", Email: "lin@example.org", DisplayName: "Lin", OrgUnitExternalID: "ou-infer", TeamExternalIDs: []string{"g-all", "g-core"}},
			{ExternalID: "u-chen", Username: "chen", DisplayName: "Chen", OrgUnitExternalID: "ou-market"},
			{ExternalID: "u-wang", Username: "wang", Email: "wang@example.org", OrgUnitExternalID: "ou-rnd", TeamExternalIDs: []string{"g-all"}},
			{ExternalID: "u-off", Username: "off", OrgUnitExternalID: "ou-rnd", Disabled: true},
		},
	}
}

type harness struct {
	store  *fakeStore
	gotrue *fakeGoTrue
	dir    *fakeDirectory
	syncer *Syncer
}

func newHarness() *harness {
	store := newFakeStore()
	gotrue := newFakeGoTrue(store)

	return &harness{
		store:  store,
		gotrue: gotrue,
		dir:    &fakeDirectory{snapshot: testSnapshot()},
		syncer: &Syncer{Store: store, Auth: gotrue},
	}
}

func (h *harness) run(t *testing.T) (Result, error) {
	t.Helper()
	return h.syncer.Run(context.Background(), testSource, h.dir)
}

func (h *harness) orgUnit(externalID string) *v1.OrgUnit {
	for _, ou := range h.store.orgUnits {
		if ou.Spec.ExternalID == externalID {
			return ou
		}
	}

	return nil
}

func (h *harness) team(externalID string) *v1.Team {
	for _, t := range h.store.teams {
		if t.Spec.ExternalID == externalID {
			return t
		}
	}

	return nil
}

func (h *harness) user(t *testing.T, externalID string) *types.User {
	t.Helper()

	link, ok := h.store.links[externalID]
	require.True(t, ok, "user %s has no link", externalID)

	return h.gotrue.users[uuid.MustParse(link.UserID)]
}

func (h *harness) directoryUser(externalID string) *orgsync.User {
	for i := range h.dir.snapshot.Users {
		if h.dir.snapshot.Users[i].ExternalID == externalID {
			return &h.dir.snapshot.Users[i]
		}
	}

	return nil
}

func TestObjectName(t *testing.T) {
	name := ObjectName("corp-ldap", "8F6C0E5E-1B0B-4A4B-9C1D-2F3E4D5C6B7A")

	assert.Equal(t, name, ObjectName("corp-ldap", "8F6C0E5E-1B0B-4A4B-9C1D-2F3E4D5C6B7A"))
	assert.NotEqual(t, name, ObjectName("corp-ldap", "another"))
	assert.Regexp(t, `^corp-ldap-[0-9a-f]{12}$`, name)
	assert.LessOrEqual(t, len(ObjectName("a234567890123456789012345678901b", "x")), 63)
}

func TestRun_FirstSyncCreatesEverything(t *testing.T) {
	h := newHarness()

	result, err := h.run(t)
	require.NoError(t, err)

	// 4 org units + 2 teams + 3 users; ot.off is disabled and never created.
	assert.Equal(t, 9, result.Created)
	assert.Zero(t, result.Pending())
	assert.Equal(t, 3+3, result.Memberships) // 3 departments, lin x2 + wang x1 teams

	rnd, ml, infer := h.orgUnit("ou-rnd"), h.orgUnit("ou-ml"), h.orgUnit("ou-infer")
	require.NotNil(t, infer)
	assert.Empty(t, rnd.Spec.Parent)
	assert.Equal(t, rnd.Metadata.Name, ml.Spec.Parent)
	assert.Equal(t, ml.Metadata.Name, infer.Spec.Parent)
	assert.Equal(t, ObjectName(testSource, "ou-infer"), infer.Metadata.Name)
	assert.Equal(t, "infer", infer.Metadata.DisplayName)
	assert.Equal(t, testSource, infer.Spec.IdentitySource)

	assert.NotContains(t, h.store.links, "u-off")
	assert.Len(t, h.gotrue.users, 3)

	lin := h.user(t, "u-lin")
	assert.Equal(t, auth.LDAPPlaceholderEmail(testSource, "u-lin"), lin.Email)
	assert.Equal(t, "ldap", lin.AppMetadata[auth.IdentitySourceKey])
	assert.Equal(t, &storage.ExternalIdentity{
		Source: testLinkSource, ExternalID: "u-lin", UserID: lin.ID.String(), Username: "lin", Email: "lin@example.org",
	}, h.store.links["u-lin"])
	assert.Equal(t, storage.OrgUnitMember{UserID: lin.ID.String(), OrgUnitID: infer.ID, IdentitySource: testSource},
		h.store.ouMembers[lin.ID.String()])
	assert.True(t, h.store.teamMembers[[2]string{strconv.Itoa(h.team("g-core").ID), lin.ID.String()}])
	assert.True(t, h.store.teamMembers[[2]string{strconv.Itoa(h.team("g-all").ID), lin.ID.String()}])

	// No display name in the directory: the profile shows the username.
	wang := h.user(t, "u-wang")
	assert.Equal(t, "wang", h.store.profiles[wang.ID.String()].Metadata.DisplayName)
}

func TestRun_SecondSyncWritesNothing(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	writes := h.store.writes

	result, err := h.run(t)
	require.NoError(t, err)

	assert.Zero(t, result.Planned)
	assert.Equal(t, writes, h.store.writes)
}

func TestRun_RenameKeepsIdentity(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	infer := h.orgUnit("ou-infer")
	inferID, inferName, created := infer.ID, infer.Metadata.Name, infer.Metadata.CreationTimestamp
	lin := h.user(t, "u-lin")

	h.dir.snapshot.OrgUnits[0].DisplayName = "inference"
	h.directoryUser("u-lin").DisplayName = "Lin Wei"
	h.directoryUser("u-lin").Email = "lin.wei@example.org"

	result, err := h.run(t)
	require.NoError(t, err)

	assert.Equal(t, 2, result.Updated)
	assert.Zero(t, result.Created)

	infer = h.orgUnit("ou-infer")
	assert.Equal(t, inferID, infer.ID)
	assert.Equal(t, inferName, infer.Metadata.Name)
	assert.Equal(t, "inference", infer.Metadata.DisplayName)
	assert.Equal(t, created, infer.Metadata.CreationTimestamp)
	assert.Equal(t, h.orgUnit("ou-ml").Metadata.Name, infer.Spec.Parent, "the update must carry the whole spec")

	assert.Equal(t, lin.ID, h.user(t, "u-lin").ID)
	profile := h.store.profiles[lin.ID.String()]
	assert.Equal(t, "Lin Wei", profile.Metadata.DisplayName)
	assert.Equal(t, "lin.wei@example.org", profile.Spec.Email)
	assert.Equal(t, "2026-01-01T00:00:00Z", profile.Metadata.CreationTimestamp, "the update must carry the whole metadata")
	assert.Equal(t, "lin.wei@example.org", h.user(t, "u-lin").UserMetadata["email"])
	assert.Equal(t, "lin.wei@example.org", h.store.links["u-lin"].Email)
}

func TestRun_DisableBansAndEnableUnbans(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	h.directoryUser("u-chen").Disabled = true

	result, err := h.run(t)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deactivated)

	chen := h.user(t, "u-chen")
	assert.True(t, auth.IsBanned(chen, time.Now()))
	assert.Equal(t, storage.SyncDeactivatedBanned, h.store.links["u-chen"].SyncDeactivated)

	// Disabled and still deactivated: nothing to do.
	result, err = h.run(t)
	require.NoError(t, err)
	assert.Zero(t, result.Planned)

	h.directoryUser("u-chen").Disabled = false

	result, err = h.run(t)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Reactivated)
	assert.False(t, auth.IsBanned(h.user(t, "u-chen"), time.Now()))
	assert.Empty(t, h.store.links["u-chen"].SyncDeactivated)
	assert.Equal(t, 1, h.gotrue.unbans)
}

func TestRun_GoneUserIsDeactivatedNotDeleted(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	lin := h.user(t, "u-lin")
	h.dir.snapshot.Users = h.dir.snapshot.Users[1:]

	result, err := h.run(t)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Deactivated)
	assert.Contains(t, h.gotrue.users, lin.ID)
	assert.True(t, auth.IsBanned(h.user(t, "u-lin"), time.Now()))
	assert.NotContains(t, h.store.ouMembers, lin.ID.String())
	assert.Contains(t, h.store.profiles, lin.ID.String())
}

// A user banned for another reason is never unbanned by the sync, whether
// the directory keeps it enabled or disables and re-enables it.
func TestRun_LeavesOtherBansAlone(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	until := time.Now().Add(24 * time.Hour)
	h.user(t, "u-chen").BannedUntil = &until

	result, err := h.run(t)
	require.NoError(t, err)
	assert.Zero(t, result.Planned, "an enabled user banned elsewhere is not reactivated")

	h.directoryUser("u-chen").Disabled = true

	result, err = h.run(t)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Deactivated)
	assert.Equal(t, storage.SyncDeactivatedKept, h.store.links["u-chen"].SyncDeactivated)
	assert.Zero(t, h.gotrue.bans)

	h.directoryUser("u-chen").Disabled = false

	result, err = h.run(t)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Reactivated)
	assert.Zero(t, h.gotrue.unbans)
	assert.True(t, auth.IsBanned(h.user(t, "u-chen"), time.Now()))
	assert.Empty(t, h.store.links["u-chen"].SyncDeactivated)
}

func TestRun_GoneDepartmentAndTeamBecomeInactive(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	h.dir.snapshot.OrgUnits = h.dir.snapshot.OrgUnits[:3] // market is gone
	h.dir.snapshot.Teams = h.dir.snapshot.Teams[:1]       // all is gone
	h.directoryUser("u-chen").OrgUnitExternalID = ""
	h.directoryUser("u-lin").TeamExternalIDs = []string{"g-core"}
	h.directoryUser("u-wang").TeamExternalIDs = nil

	result, err := h.run(t)
	require.NoError(t, err)

	assert.Equal(t, 2, result.Deactivated)
	assert.Equal(t, v1.OrgPhaseInactive, h.orgUnit("ou-market").Status.Phase)
	assert.Equal(t, v1.OrgPhaseInactive, h.team("g-all").Status.Phase)
	assert.NotContains(t, h.store.ouMembers, h.user(t, "u-chen").ID.String())

	// Back in the directory: reactivated, same row.
	h.dir.snapshot = testSnapshot()
	id := h.orgUnit("ou-market").ID

	result, err = h.run(t)
	require.NoError(t, err)
	assert.Equal(t, 2, result.Reactivated)
	assert.Equal(t, id, h.orgUnit("ou-market").ID)
	assert.Equal(t, v1.OrgPhaseActive, h.orgUnit("ou-market").Status.Phase)
}

func TestRun_DirectoryFailureWritesNothing(t *testing.T) {
	h := newHarness()

	_, err := h.run(t)
	require.NoError(t, err)

	writes := h.store.writes
	h.dir.err = errors.New("ldap: no such object")

	result, err := h.run(t)

	require.ErrorIs(t, err, ErrReadDirectory)
	assert.Equal(t, Result{}, result)
	assert.Equal(t, writes, h.store.writes)
}

func TestRun_InconsistentSnapshotWritesNothing(t *testing.T) {
	h := newHarness()
	h.dir.snapshot.Users[0].OrgUnitExternalID = "ou-unknown"

	_, err := h.run(t)

	require.ErrorIs(t, err, ErrLoadState)
	assert.Zero(t, h.store.writes)
}

// A write that fails stops the sync where it is; the counts say how far it
// got, and the next sync finishes the job.
func TestRun_PartialFailureResumes(t *testing.T) {
	h := newHarness()
	h.store.failAt = 5 // the fifth write: the first team

	result, err := h.run(t)

	require.ErrorIs(t, err, errInjected)
	assert.Contains(t, err.Error(), "team")
	assert.Equal(t, 4, result.Created)
	assert.Equal(t, 15-4, result.Pending())

	h.store.failAt = 0

	result, err = h.run(t)
	require.NoError(t, err)
	assert.Equal(t, 5, result.Created)
	assert.Zero(t, result.Pending())

	result, err = h.run(t)
	require.NoError(t, err)
	assert.Zero(t, result.Planned)
}

// A user that logged in before the sync existed has a link without the
// synced username and email; the first sync updates it instead of creating
// another user.
func TestRun_ClaimsUserCreatedByLogin(t *testing.T) {
	h := newHarness()

	users := &auth.ExternalUsers{Client: h.gotrue, Links: h.store}
	userID, created, err := users.Ensure(context.Background(), auth.LDAPAccount(testSource, "u-lin", "lin", "Lin", "lin@example.org"))
	require.NoError(t, err)
	require.True(t, created)

	h.store.links["u-lin"].Username, h.store.links["u-lin"].Email = "", ""

	result, err := h.run(t)
	require.NoError(t, err)

	assert.Equal(t, 1, result.Updated)
	assert.Equal(t, 8, result.Created)
	assert.Equal(t, userID, h.store.links["u-lin"].UserID)
	assert.Equal(t, "lin", h.store.links["u-lin"].Username)
	assert.Len(t, h.gotrue.users, 3)
}

func TestRun_StopsWhenContextIsDone(t *testing.T) {
	h := newHarness()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := h.syncer.Run(ctx, testSource, h.dir)

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, h.store.writes)
}

func TestResult(t *testing.T) {
	r := Result{Planned: 10, Created: 2, Updated: 1, Reactivated: 1, Deactivated: 1, Memberships: 3}

	assert.Equal(t, 8, r.Applied())
	assert.Equal(t, 2, r.Pending())
	assert.Equal(t, "8/10", fmt.Sprintf("%d/%d", r.Applied(), r.Planned))
}
