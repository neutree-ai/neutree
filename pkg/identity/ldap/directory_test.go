package ldap

import (
	"context"
	"errors"
	"strconv"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
)

const (
	dirBase      = "dc=example,dc=org"
	dirOUBase    = "ou=org,dc=example,dc=org"
	dirGroupBase = "ou=groups,dc=example,dc=org"
	dirUserBase  = "ou=org,dc=example,dc=org"
	disabledFlt  = "(employeeType=disabled)"
)

// fakeDir answers subtree searches from a fixed list of results keyed by base
// DN and filter, in pages of pageSize, and base searches for ranged attributes
// from ranges. failAt makes the n-th Search call (1-based) fail.
type fakeDir struct {
	goldap.Client

	results  map[string][]*goldap.Entry
	ranges   map[string]*goldap.Entry
	failAt   int
	failErr  error
	bindErr  error
	searches []*goldap.SearchRequest
	closed   int
}

func (f *fakeDir) Bind(string, string) error { return f.bindErr }

func (f *fakeDir) Close() error {
	f.closed++
	return nil
}

func (f *fakeDir) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	f.searches = append(f.searches, req)
	if f.failAt == len(f.searches) {
		return &goldap.SearchResult{}, f.failErr
	}

	if req.Scope == goldap.ScopeBaseObject {
		e, ok := f.ranges[req.BaseDN+"|"+req.Attributes[0]]
		if !ok {
			return &goldap.SearchResult{}, nil
		}

		return result(e), nil
	}

	all := f.results[req.BaseDN+"|"+req.Filter]

	paging, _ := goldap.FindControl(req.Controls, goldap.ControlTypePaging).(*goldap.ControlPaging)
	if paging == nil {
		return result(all...), nil
	}

	start := 0
	if len(paging.Cookie) > 0 {
		start, _ = strconv.Atoi(string(paging.Cookie))
	}

	end := min(start+int(paging.PagingSize), len(all))
	res := result(all[start:end]...)
	next := goldap.NewControlPaging(paging.PagingSize)

	if end < len(all) {
		next.SetCookie([]byte(strconv.Itoa(end)))
	}

	res.Controls = []goldap.Control{next}

	return res, nil
}

func ouE(dn, id, name string) *goldap.Entry {
	return goldap.NewEntry(dn, map[string][]string{"entryUUID": {id}, "ou": {name}})
}

func groupE(dn, id, name string, members ...string) *goldap.Entry {
	return goldap.NewEntry(dn, map[string][]string{"entryUUID": {id}, "cn": {name}, "member": members})
}

func userE(dn, id, uid, name string) *goldap.Entry {
	return goldap.NewEntry(dn, map[string][]string{"entryUUID": {id}, "uid": {uid}, "cn": {name}, "mail": {uid + "@example.org"}})
}

func dirConfig() Config {
	cfg := openLDAPConfig()
	cfg.UserBaseDN = dirUserBase
	cfg.Sync = SyncConfig{
		OrgUnitBaseDN:      dirOUBase,
		GroupBaseDN:        dirGroupBase,
		DisabledUserFilter: disabledFlt,
		PageSize:           2,
	}

	return cfg
}

const userListFilter = "(&(objectClass=inetOrgPerson)(uid=*))"

// fixture: org > (eng > platform), (sales) ; a user directly under the base
// and one outside every OU; groups dev ⊃ core ⊃ dev (cycle), plus a member
// outside the synced users.
func fixtureDir() *fakeDir {
	return &fakeDir{
		results: map[string][]*goldap.Entry{
			dirOUBase + "|" + DefaultOrgUnitFilter: {
				ouE("ou=org,dc=example,dc=org", "ou-org", "org"),
				ouE("OU=Eng,ou=org,dc=example,dc=org", "ou-eng", "Engineering"),
				ouE("ou=platform,ou=eng,ou=org,dc=example,dc=org", "ou-plat", "Platform"),
				ouE("ou=sales,ou=org,dc=example,dc=org", "ou-sales", ""),
			},
			dirGroupBase + "|" + DefaultGroupFilter: {
				groupE("cn=dev,ou=groups,dc=example,dc=org", "g-dev", "Developers",
					"uid=alice,ou=platform,ou=eng,ou=org,dc=example,dc=org",
					"cn=core,ou=groups,dc=example,dc=org",
					"uid=ghost,ou=elsewhere,dc=example,dc=org"),
				groupE("cn=core,ou=groups,dc=example,dc=org", "g-core", "Core",
					"UID=Bob,OU=Eng,OU=Org,DC=example,DC=org",
					"cn=dev,ou=groups,dc=example,dc=org"),
				groupE("cn=empty,ou=groups,dc=example,dc=org", "g-empty", "Empty"),
			},
			dirUserBase + "|" + userListFilter: {
				userE("uid=alice,ou=platform,ou=eng,ou=org,dc=example,dc=org", "u-alice", "alice", "Alice"),
				userE("uid=bob,ou=eng,ou=org,dc=example,dc=org", "u-bob", "bob", "Bob"),
				userE("uid=carol,ou=sales,ou=org,dc=example,dc=org", "u-carol", "carol", "Carol"),
				userE("uid=root,ou=org,dc=example,dc=org", "u-root", "root", "Root"),
				userE("uid=nobody,cn=users,ou=org,dc=example,dc=org", "u-nobody", "nobody", "Nobody"),
			},
			dirUserBase + "|(&" + userListFilter + disabledFlt + ")": {
				goldap.NewEntry("uid=carol,ou=sales,ou=org,dc=example,dc=org", nil),
			},
		},
	}
}

func snapshot(t *testing.T, cfg Config, conn *fakeDir) (*orgsync.Snapshot, error) {
	t.Helper()

	d, err := NewDirectory(cfg, func(context.Context) (goldap.Client, error) { return conn, nil })
	require.NoError(t, err)

	return d.Snapshot(context.Background())
}

func TestSnapshot(t *testing.T) {
	conn := fixtureDir()
	snap, err := snapshot(t, dirConfig(), conn)
	require.NoError(t, err)
	assert.Equal(t, 1, conn.closed)

	assert.Equal(t, []orgsync.OrgUnit{
		{ExternalID: "ou-org", DisplayName: "org", DN: "ou=org,dc=example,dc=org"},
		{ExternalID: "ou-eng", ParentExternalID: "ou-org", DisplayName: "Engineering", DN: "OU=Eng,ou=org,dc=example,dc=org"},
		{ExternalID: "ou-plat", ParentExternalID: "ou-eng", DisplayName: "Platform", DN: "ou=platform,ou=eng,ou=org,dc=example,dc=org"},
		// missing name attribute falls back to the RDN value
		{ExternalID: "ou-sales", ParentExternalID: "ou-org", DisplayName: "sales", DN: "ou=sales,ou=org,dc=example,dc=org"},
	}, snap.OrgUnits)

	assert.Equal(t, []orgsync.Team{
		{ExternalID: "g-dev", DisplayName: "Developers", DN: "cn=dev,ou=groups,dc=example,dc=org"},
		{ExternalID: "g-core", DisplayName: "Core", DN: "cn=core,ou=groups,dc=example,dc=org"},
		{ExternalID: "g-empty", DisplayName: "Empty", DN: "cn=empty,ou=groups,dc=example,dc=org"},
	}, snap.Teams)

	byID := map[string]orgsync.User{}
	for _, u := range snap.Users {
		byID[u.ExternalID] = u
	}

	require.Len(t, byID, 5)

	alice := byID["u-alice"]
	assert.Equal(t, "alice", alice.Username)
	assert.Equal(t, "alice@example.org", alice.Email)
	assert.Equal(t, "Alice", alice.DisplayName)
	assert.Equal(t, "ou-plat", alice.OrgUnitExternalID)
	// alice is in dev directly and in core through the dev ⊂ core ⊂ dev cycle
	assert.Equal(t, []string{"g-core", "g-dev"}, alice.TeamExternalIDs)
	assert.False(t, alice.Disabled)

	bob := byID["u-bob"]
	assert.Equal(t, "ou-eng", bob.OrgUnitExternalID)
	assert.Equal(t, []string{"g-core", "g-dev"}, bob.TeamExternalIDs, "DN match is case-insensitive")

	assert.True(t, byID["u-carol"].Disabled)
	assert.Empty(t, byID["u-carol"].TeamExternalIDs)
	assert.Equal(t, "ou-org", byID["u-root"].OrgUnitExternalID)
	assert.Equal(t, "ou-org", byID["u-nobody"].OrgUnitExternalID, "a non-OU container is skipped")

	// paging: 4 OUs, 3 groups, 5 users at page size 2 → 2+2+3 pages, 1 disabled page
	assert.Len(t, conn.searches, 8)

	for _, req := range conn.searches[:7] {
		assert.Equal(t, goldap.ScopeWholeSubtree, req.Scope)
		assert.Contains(t, req.Attributes, "entryUUID")
	}

	assert.Equal(t, []string{"1.1"}, conn.searches[7].Attributes, "disabled search reads DNs only")

	// a valid snapshot is a valid Diff input
	_, err = orgsync.Diff(snap, nil)
	require.NoError(t, err)
}

func TestSnapshotUserWithoutOrgUnit(t *testing.T) {
	conn := fixtureDir()
	cfg := dirConfig()
	cfg.Sync.OrgUnitBaseDN = ""
	cfg.Sync.GroupBaseDN = ""
	cfg.Sync.DisabledUserFilter = ""

	snap, err := snapshot(t, cfg, conn)
	require.NoError(t, err)
	assert.Empty(t, snap.OrgUnits)
	assert.Empty(t, snap.Teams)
	require.Len(t, snap.Users, 5)

	for _, u := range snap.Users {
		assert.Empty(t, u.OrgUnitExternalID)
		assert.Empty(t, u.TeamExternalIDs)
		assert.False(t, u.Disabled)
	}

	assert.Len(t, conn.searches, 3, "only users are read")
}

func TestSnapshotRangedMembers(t *testing.T) {
	groupDN := "cn=big,ou=groups,dc=example,dc=org"
	alice := "uid=alice,ou=platform,ou=eng,ou=org,dc=example,dc=org"
	bob := "uid=bob,ou=eng,ou=org,dc=example,dc=org"

	conn := fixtureDir()
	conn.results[dirGroupBase+"|"+DefaultGroupFilter] = []*goldap.Entry{
		goldap.NewEntry(groupDN, map[string][]string{"entryUUID": {"g-big"}, "cn": {"big"}, "member;range=0-0": {alice}}),
	}
	conn.ranges = map[string]*goldap.Entry{
		groupDN + "|member;range=1-*": goldap.NewEntry(groupDN, map[string][]string{"member;range=1-*": {bob}}),
	}

	snap, err := snapshot(t, dirConfig(), conn)
	require.NoError(t, err)

	for _, u := range snap.Users {
		switch u.ExternalID {
		case "u-alice", "u-bob":
			assert.Equal(t, []string{"g-big"}, u.TeamExternalIDs, u.ExternalID)
		default:
			assert.Empty(t, u.TeamExternalIDs, u.ExternalID)
		}
	}

	t.Run("range read without a range is an error", func(t *testing.T) {
		conn := fixtureDir()
		conn.results[dirGroupBase+"|"+DefaultGroupFilter] = []*goldap.Entry{
			goldap.NewEntry(groupDN, map[string][]string{"entryUUID": {"g-big"}, "member;range=0-0": {alice}}),
		}
		conn.ranges = map[string]*goldap.Entry{groupDN + "|member;range=1-*": goldap.NewEntry(groupDN, nil)}

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrIncompleteRead)
	})
}

func TestSnapshotErrorsReturnNothing(t *testing.T) {
	netErr := goldap.NewError(goldap.ErrorNetwork, errors.New("connection reset"))

	// Search calls: OUs 1-2, groups 3-4, users 5-7, disabled 8.
	for _, failAt := range []int{1, 2, 4, 6, 7, 8} {
		t.Run("search "+strconv.Itoa(failAt)+" fails", func(t *testing.T) {
			conn := fixtureDir()
			conn.failAt = failAt
			conn.failErr = netErr

			snap, err := snapshot(t, dirConfig(), conn)
			assert.Nil(t, snap)
			assert.ErrorIs(t, err, ErrConnection)
			assert.Len(t, conn.searches, failAt, "reading stops at the first failure")
			assert.Equal(t, 1, conn.closed)
		})
	}

	t.Run("server error mid-paging", func(t *testing.T) {
		conn := fixtureDir()
		conn.failAt = 6
		conn.failErr = goldap.NewError(goldap.LDAPResultSizeLimitExceeded, errors.New("size limit"))

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		require.Error(t, err)
		assert.True(t, goldap.IsErrorWithCode(err, goldap.LDAPResultSizeLimitExceeded))
	})

	t.Run("time limit", func(t *testing.T) {
		conn := fixtureDir()
		conn.failAt = 3
		conn.failErr = goldap.NewError(goldap.LDAPResultTimeLimitExceeded, errors.New("time limit"))

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		assert.Error(t, err)
	})

	t.Run("service bind", func(t *testing.T) {
		conn := fixtureDir()
		conn.bindErr = goldap.NewError(goldap.LDAPResultInvalidCredentials, errors.New("bad"))

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrServiceBindFailed)
	})

	t.Run("dial", func(t *testing.T) {
		d, err := NewDirectory(dirConfig(), func(context.Context) (goldap.Client, error) { return nil, errors.New("refused") })
		require.NoError(t, err)

		snap, err := d.Snapshot(context.Background())
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrConnection)
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		d, err := NewDirectory(dirConfig(), func(context.Context) (goldap.Client, error) { return fixtureDir(), nil })
		require.NoError(t, err)

		snap, err := d.Snapshot(ctx)
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrConnection)
		assert.ErrorIs(t, err, context.Canceled)
	})

	t.Run("user without ID", func(t *testing.T) {
		conn := fixtureDir()
		conn.results[dirUserBase+"|"+userListFilter][2] = goldap.NewEntry("uid=x,ou=org,dc=example,dc=org", map[string][]string{"uid": {"x"}})

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrMissingAttribute)
	})

	t.Run("user without username", func(t *testing.T) {
		conn := fixtureDir()
		conn.results[dirUserBase+"|"+userListFilter][2] = goldap.NewEntry("uid=x,ou=org,dc=example,dc=org", map[string][]string{"entryUUID": {"u-x"}})

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrMissingAttribute)
	})

	t.Run("duplicate ID", func(t *testing.T) {
		conn := fixtureDir()
		conn.results[dirUserBase+"|"+userListFilter][2] = userE("uid=x,ou=org,dc=example,dc=org", "u-alice", "x", "X")

		snap, err := snapshot(t, dirConfig(), conn)
		assert.Nil(t, snap)
		assert.ErrorIs(t, err, ErrIncompleteRead)
	})
}

func TestSnapshotActiveDirectory(t *testing.T) {
	cfg := adConfig()
	cfg.UserBaseDN = dirBase
	cfg.Sync = SyncConfig{OrgUnitBaseDN: dirBase, DisabledUserFilter: ADDisabledUserFilter}

	listFilter := "(&(objectClass=user)(sAMAccountName=*))"
	userDN := "CN=Alice,OU=Eng,DC=example,DC=org"
	conn := &fakeDir{results: map[string][]*goldap.Entry{
		dirBase + "|" + DefaultOrgUnitFilter: {
			goldap.NewEntry("OU=Eng,DC=example,DC=org", map[string][]string{"objectGUID": {adGUID}, "ou": {"Eng"}}),
		},
		dirBase + "|" + listFilter: {
			goldap.NewEntry(userDN, map[string][]string{
				"objectGUID":     {string([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})},
				"sAMAccountName": {"alice"},
				"displayName":    {"Alice L."},
			}),
		},
		dirBase + "|(&" + listFilter + ADDisabledUserFilter + ")": {goldap.NewEntry(userDN, nil)},
	}}

	snap, err := snapshot(t, cfg, conn)
	require.NoError(t, err)
	require.Len(t, snap.OrgUnits, 1)
	assert.Equal(t, "6f9619ff-8b86-d011-b42d-00c04fc964ff", snap.OrgUnits[0].ExternalID)
	require.Len(t, snap.Users, 1)
	assert.Equal(t, "04030201-0605-0807-090a-0b0c0d0e0f10", snap.Users[0].ExternalID)
	assert.Equal(t, snap.OrgUnits[0].ExternalID, snap.Users[0].OrgUnitExternalID)
	assert.True(t, snap.Users[0].Disabled)
}

func TestNewDirectoryValidation(t *testing.T) {
	dial := func(context.Context) (goldap.Client, error) { return nil, nil }

	cases := map[string]func(*Config){
		"bad org unit base":    func(c *Config) { c.Sync.OrgUnitBaseDN = "not a dn" },
		"bad group filter":     func(c *Config) { c.Sync.GroupFilter = "(cn=" },
		"bad disabled filter":  func(c *Config) { c.Sync.DisabledUserFilter = "employeeType=disabled)" },
		"list with username":   func(c *Config) { c.Sync.UserListFilter = "(uid={username})" },
		"auth config invalid":  func(c *Config) { c.BindDN = "" },
		"bad user list filter": func(c *Config) { c.Sync.UserListFilter = "((" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := dirConfig()
			mutate(&cfg)

			_, err := NewDirectory(cfg, dial)
			assert.ErrorIs(t, err, ErrInvalidConfig)
		})
	}

	_, err := NewDirectory(dirConfig(), nil)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}
