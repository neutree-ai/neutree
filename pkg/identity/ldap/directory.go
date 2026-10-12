package ldap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
)

// Defaults of SyncConfig.
const (
	DefaultOrgUnitFilter        = "(objectClass=organizationalUnit)"
	DefaultOrgUnitNameAttribute = "ou"
	DefaultGroupFilter          = "(|(objectClass=groupOfNames)(objectClass=groupOfUniqueNames)(objectClass=group))"
	DefaultGroupNameAttribute   = "cn"
	DefaultGroupMemberAttribute = "member"
	DefaultPageSize             = 500

	// ADDisabledUserFilter matches Active Directory accounts with the
	// ACCOUNTDISABLE bit (0x2) of userAccountControl set.
	ADDisabledUserFilter = "(userAccountControl:1.2.840.113556.1.4.803:=2)"
)

// ErrIncompleteRead means the directory answered, but not with everything
// asked for (for example a ranged attribute that cannot be followed). The
// snapshot is discarded rather than returned partially.
var ErrIncompleteRead = errors.New("ldap: incomplete directory read")

// SyncConfig says where departments, groups and users are for an organization
// sync. Users are read from Config.UserBaseDN; IDs come from
// Config.Attributes.ID for every kind of entry.
type SyncConfig struct {
	// OrgUnitBaseDN is the subtree whose organizational units become
	// departments. Empty reads no departments.
	OrgUnitBaseDN string
	// OrgUnitFilter defaults to DefaultOrgUnitFilter.
	OrgUnitFilter string
	// OrgUnitNameAttribute is the display name of a department; defaults to "ou".
	OrgUnitNameAttribute string

	// GroupBaseDN is the subtree whose groups become teams. Empty reads no teams.
	GroupBaseDN string
	// GroupFilter defaults to DefaultGroupFilter.
	GroupFilter string
	// GroupNameAttribute is the display name of a team; defaults to "cn".
	GroupNameAttribute string
	// GroupMemberAttribute holds member DNs (users or nested groups); defaults
	// to "member". Use "uniqueMember" for groupOfUniqueNames.
	GroupMemberAttribute string

	// UserListFilter selects every user to sync. Empty derives it from
	// Config.UserFilter by replacing UsernamePlaceholder with "*".
	UserListFilter string
	// DisabledUserFilter selects the disabled users among those. Empty means
	// no user is treated as disabled. For Active Directory use
	// ADDisabledUserFilter; OpenLDAP has no standard flag, so name whatever
	// the directory uses, e.g. "(pwdAccountLockedTime=*)" with ppolicy.
	DisabledUserFilter string

	// PageSize is the paged-results page size; defaults to DefaultPageSize.
	PageSize uint32
}

func (s SyncConfig) withDefaults(userFilter string) SyncConfig {
	if s.OrgUnitFilter == "" {
		s.OrgUnitFilter = DefaultOrgUnitFilter
	}

	if s.OrgUnitNameAttribute == "" {
		s.OrgUnitNameAttribute = DefaultOrgUnitNameAttribute
	}

	if s.GroupFilter == "" {
		s.GroupFilter = DefaultGroupFilter
	}

	if s.GroupNameAttribute == "" {
		s.GroupNameAttribute = DefaultGroupNameAttribute
	}

	if s.GroupMemberAttribute == "" {
		s.GroupMemberAttribute = DefaultGroupMemberAttribute
	}

	if s.UserListFilter == "" {
		s.UserListFilter = strings.ReplaceAll(userFilter, UsernamePlaceholder, "*")
	}

	if s.PageSize == 0 {
		s.PageSize = DefaultPageSize
	}

	return s
}

func (s SyncConfig) validate() error {
	for _, dn := range []struct{ name, value string }{
		{"org unit base DN", s.OrgUnitBaseDN},
		{"group base DN", s.GroupBaseDN},
	} {
		if dn.value == "" {
			continue
		}

		if _, err := goldap.ParseDN(dn.value); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalidConfig, dn.name, err)
		}
	}

	for _, f := range []struct{ name, value string }{
		{"org unit filter", s.OrgUnitFilter},
		{"group filter", s.GroupFilter},
		{"user list filter", s.UserListFilter},
		{"disabled user filter", s.DisabledUserFilter},
	} {
		if f.value == "" {
			continue
		}

		if _, err := goldap.CompileFilter(f.value); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalidConfig, f.name, err)
		}
	}

	if strings.Contains(s.UserListFilter, UsernamePlaceholder) {
		return fmt.Errorf("%w: user list filter must not contain %s", ErrInvalidConfig, UsernamePlaceholder)
	}

	return nil
}

// Directory reads a whole directory for an organization sync.
type Directory struct {
	cfg  Config
	sync SyncConfig
	dial Dialer
}

// NewDirectory validates cfg (including cfg.Sync) and returns a Directory
// using dial to reach the directory.
func NewDirectory(cfg Config, dial Dialer) (*Directory, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if dial == nil {
		return nil, fmt.Errorf("%w: dialer is required", ErrInvalidConfig)
	}

	sc := cfg.Sync.withDefaults(cfg.UserFilter)
	if err := sc.validate(); err != nil {
		return nil, err
	}

	return &Directory{cfg: cfg, sync: sc, dial: dial}, nil
}

// Snapshot reads departments, groups and users in one connection and returns
// the whole directory. Any failure (connection, bind, a search or a page of
// it, a malformed entry) returns an error and no snapshot.
//
// Departments are the entries under OrgUnitBaseDN; the parent of a department
// and the primary department of a user are the nearest ancestor DN that is a
// department. A user with no such ancestor has no department. Group members
// are resolved through GroupMemberAttribute: user DNs become memberships,
// group DNs are expanded recursively (cycles are tolerated), and DNs outside
// the synced users and groups are ignored.
func (d *Directory) Snapshot(ctx context.Context) (*orgsync.Snapshot, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		if errors.Is(err, ErrConnection) || errors.Is(err, ErrTLS) || errors.Is(err, ErrInvalidConfig) {
			return nil, err
		}

		return nil, connectionError("dial", err)
	}

	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }

	defer closeConn()

	stop := context.AfterFunc(ctx, closeConn)
	defer stop()

	if err := conn.Bind(d.cfg.BindDN, d.cfg.BindPassword); err != nil {
		return nil, d.opError(ctx, ErrServiceBindFailed, "service bind", err)
	}

	r := &reader{d: d, ctx: ctx, conn: conn}

	return r.read()
}

func (d *Directory) opError(ctx context.Context, sentinel error, op string, err error) error {
	return (&Authenticator{cfg: d.cfg}).operationError(ctx, sentinel, op, err)
}

type reader struct {
	d    *Directory
	ctx  context.Context
	conn goldap.Client
}

type ouEntry struct {
	key  string
	dn   *goldap.DN
	unit orgsync.OrgUnit
}

type groupEntry struct {
	key     string
	team    orgsync.Team
	members []string // normalized member DNs
}

type userEntry struct {
	key  string
	dn   *goldap.DN
	user orgsync.User
}

func (r *reader) read() (*orgsync.Snapshot, error) {
	ous, err := r.readOrgUnits()
	if err != nil {
		return nil, err
	}

	groups, err := r.readGroups()
	if err != nil {
		return nil, err
	}

	users, err := r.readUsers()
	if err != nil {
		return nil, err
	}

	disabled, err := r.readDisabled()
	if err != nil {
		return nil, err
	}

	ouByKey := make(map[string]*ouEntry, len(ous))
	for _, ou := range ous {
		ouByKey[ou.key] = ou
	}

	snap := &orgsync.Snapshot{
		OrgUnits: make([]orgsync.OrgUnit, 0, len(ous)),
		Teams:    make([]orgsync.Team, 0, len(groups)),
		Users:    make([]orgsync.User, 0, len(users)),
	}

	for _, ou := range ous {
		if parent := nearestOrgUnit(ou.dn, ouByKey); parent != nil {
			ou.unit.ParentExternalID = parent.unit.ExternalID
		}

		snap.OrgUnits = append(snap.OrgUnits, ou.unit)
	}

	userByKey := make(map[string]*userEntry, len(users))
	for _, u := range users {
		userByKey[u.key] = u
	}

	groupByKey := make(map[string]*groupEntry, len(groups))
	for _, g := range groups {
		groupByKey[g.key] = g
	}

	teamsOf := make(map[string][]string, len(users))

	for _, g := range groups {
		snap.Teams = append(snap.Teams, g.team)

		for _, userKey := range expandMembers(g, groupByKey, userByKey) {
			teamsOf[userKey] = append(teamsOf[userKey], g.team.ExternalID)
		}
	}

	for _, u := range users {
		if ou := nearestOrgUnit(u.dn, ouByKey); ou != nil {
			u.user.OrgUnitExternalID = ou.unit.ExternalID
		}

		teams := teamsOf[u.key]
		sort.Strings(teams)
		u.user.TeamExternalIDs = teams
		u.user.Disabled = disabled[u.key]
		snap.Users = append(snap.Users, u.user)
	}

	return snap, nil
}

func (r *reader) readOrgUnits() ([]*ouEntry, error) {
	sc := r.d.sync
	if sc.OrgUnitBaseDN == "" {
		return nil, nil
	}

	entries, err := r.searchAll("search org units", sc.OrgUnitBaseDN, sc.OrgUnitFilter, r.attrs(sc.OrgUnitNameAttribute))
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]*ouEntry, 0, len(entries))

	for _, e := range entries {
		dn, key, err := parseDN(e.DN)
		if err != nil {
			return nil, err
		}

		id, err := r.externalID(e, seen)
		if err != nil {
			return nil, err
		}

		name := e.GetEqualFoldAttributeValue(sc.OrgUnitNameAttribute)
		if name == "" {
			name = firstRDNValue(dn)
		}

		out = append(out, &ouEntry{key: key, dn: dn, unit: orgsync.OrgUnit{ExternalID: id, DisplayName: name, DN: e.DN}})
	}

	return out, nil
}

func (r *reader) readGroups() ([]*groupEntry, error) {
	sc := r.d.sync
	if sc.GroupBaseDN == "" {
		return nil, nil
	}

	entries, err := r.searchAll("search groups", sc.GroupBaseDN, sc.GroupFilter, r.attrs(sc.GroupNameAttribute, sc.GroupMemberAttribute))
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]*groupEntry, 0, len(entries))

	for _, e := range entries {
		dn, key, err := parseDN(e.DN)
		if err != nil {
			return nil, err
		}

		id, err := r.externalID(e, seen)
		if err != nil {
			return nil, err
		}

		name := e.GetEqualFoldAttributeValue(sc.GroupNameAttribute)
		if name == "" {
			name = firstRDNValue(dn)
		}

		values, err := r.memberValues(e)
		if err != nil {
			return nil, err
		}

		members := make([]string, 0, len(values))

		for _, v := range values {
			_, memberKey, err := parseDN(v)
			if err != nil {
				return nil, err
			}

			members = append(members, memberKey)
		}

		out = append(out, &groupEntry{key: key, team: orgsync.Team{ExternalID: id, DisplayName: name, DN: e.DN}, members: members})
	}

	return out, nil
}

// memberValues returns all member DNs of a group entry. Active Directory
// returns a large attribute in ranges ("member;range=0-1499"); the remaining
// ranges are fetched with base searches on the group.
func (r *reader) memberValues(e *goldap.Entry) ([]string, error) {
	attr := r.d.sync.GroupMemberAttribute
	values := e.GetEqualFoldAttributeValues(attr)

	low, high, ranged, err := rangedValues(e, attr, &values)
	if err != nil || !ranged {
		return values, err
	}

	for high != "*" {
		end, err := strconv.Atoi(high)
		if err != nil || end < low {
			return nil, fmt.Errorf("%w: group %q: bad range end %q", ErrIncompleteRead, e.DN, high)
		}

		low = end + 1
		req := goldap.NewSearchRequest(e.DN, goldap.ScopeBaseObject, goldap.NeverDerefAliases, 0, r.timeLimit(), false,
			"(objectClass=*)", []string{fmt.Sprintf("%s;range=%d-*", attr, low)}, nil)

		res, err := r.conn.Search(req)
		if err != nil {
			return nil, r.d.opError(r.ctx, nil, "read group members", err)
		}

		if len(res.Entries) != 1 {
			return nil, fmt.Errorf("%w: group %q: range read returned %d entries", ErrIncompleteRead, e.DN, len(res.Entries))
		}

		var ok bool

		_, high, ok, err = rangedValues(res.Entries[0], attr, &values)
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, fmt.Errorf("%w: group %q: range read returned no range", ErrIncompleteRead, e.DN)
		}
	}

	return values, nil
}

// rangedValues appends the values of "<attr>;range=<low>-<high>" to values and
// returns low and high ("*" for the last range). ranged is false when the entry
// has no ranged attribute.
func rangedValues(e *goldap.Entry, attr string, values *[]string) (low int, high string, ranged bool, err error) {
	prefix := strings.ToLower(attr) + ";range="

	for _, a := range e.Attributes {
		name := strings.ToLower(a.Name)
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		lowStr, highStr, ok := strings.Cut(strings.TrimPrefix(name, prefix), "-")
		if !ok {
			return 0, "", false, fmt.Errorf("%w: entry %q: bad range attribute %q", ErrIncompleteRead, e.DN, a.Name)
		}

		low, err = strconv.Atoi(lowStr)
		if err != nil {
			return 0, "", false, fmt.Errorf("%w: entry %q: bad range attribute %q", ErrIncompleteRead, e.DN, a.Name)
		}

		*values = append(*values, a.Values...)

		return low, highStr, true, nil
	}

	return 0, "", false, nil
}

func (r *reader) readUsers() ([]*userEntry, error) {
	m := r.d.cfg.Attributes

	entries, err := r.searchAll("search users", r.d.cfg.UserBaseDN, r.d.sync.UserListFilter, r.attrs(m.Username, m.Email, m.DisplayName))
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	out := make([]*userEntry, 0, len(entries))

	for _, e := range entries {
		dn, key, err := parseDN(e.DN)
		if err != nil {
			return nil, err
		}

		id, err := r.externalID(e, seen)
		if err != nil {
			return nil, err
		}

		u := orgsync.User{ExternalID: id, DN: e.DN, Username: e.GetEqualFoldAttributeValue(m.Username)}
		if u.Username == "" {
			return nil, fmt.Errorf("%w: %s of %q", ErrMissingAttribute, m.Username, e.DN)
		}

		if m.Email != "" {
			u.Email = e.GetEqualFoldAttributeValue(m.Email)
		}

		if m.DisplayName != "" {
			u.DisplayName = e.GetEqualFoldAttributeValue(m.DisplayName)
		}

		out = append(out, &userEntry{key: key, dn: dn, user: u})
	}

	return out, nil
}

// readDisabled returns the normalized DNs of disabled users.
func (r *reader) readDisabled() (map[string]bool, error) {
	if r.d.sync.DisabledUserFilter == "" {
		return nil, nil
	}

	filter := "(&" + r.d.sync.UserListFilter + r.d.sync.DisabledUserFilter + ")"

	entries, err := r.searchAll("search disabled users", r.d.cfg.UserBaseDN, filter, []string{"1.1"})
	if err != nil {
		return nil, err
	}

	disabled := make(map[string]bool, len(entries))

	for _, e := range entries {
		_, key, err := parseDN(e.DN)
		if err != nil {
			return nil, err
		}

		disabled[key] = true
	}

	return disabled, nil
}

// searchAll runs a subtree search with the paged-results control and returns
// every entry, or an error if any page fails.
func (r *reader) searchAll(op, baseDN, filter string, attrs []string) ([]*goldap.Entry, error) {
	paging := goldap.NewControlPaging(r.d.sync.PageSize)

	var entries []*goldap.Entry

	for {
		if err := r.ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrConnection, op, err)
		}

		req := goldap.NewSearchRequest(baseDN, goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, r.timeLimit(), false,
			filter, attrs, []goldap.Control{paging})

		res, err := r.conn.Search(req)
		if err != nil {
			return nil, r.d.opError(r.ctx, nil, op, err)
		}

		entries = append(entries, res.Entries...)

		ctrl, ok := goldap.FindControl(res.Controls, goldap.ControlTypePaging).(*goldap.ControlPaging)
		if !ok || len(ctrl.Cookie) == 0 {
			return entries, nil
		}

		paging.SetCookie(ctrl.Cookie)
	}
}

func (r *reader) timeLimit() int {
	return int(r.d.cfg.timeout().Seconds())
}

// attrs returns the ID attribute plus extra, without empties or duplicates.
func (r *reader) attrs(extra ...string) []string {
	out := []string{r.d.cfg.Attributes.ID}

	for _, name := range extra {
		if name == "" {
			continue
		}

		duplicate := false

		for _, existing := range out {
			if strings.EqualFold(existing, name) {
				duplicate = true
				break
			}
		}

		if !duplicate {
			out = append(out, name)
		}
	}

	return out
}

func (r *reader) externalID(e *goldap.Entry, seen map[string]bool) (string, error) {
	id, err := externalID(e, r.d.cfg.Attributes.ID)
	if err != nil {
		return "", fmt.Errorf("%w (entry %q)", err, e.DN)
	}

	if seen[id] {
		return "", fmt.Errorf("%w: duplicate %s %q", ErrIncompleteRead, r.d.cfg.Attributes.ID, id)
	}

	seen[id] = true

	return id, nil
}

// expandMembers returns the normalized DNs of every synced user in g,
// following nested groups. A group already visited is skipped, so cycles end.
func expandMembers(g *groupEntry, groups map[string]*groupEntry, users map[string]*userEntry) []string {
	visited := map[string]bool{g.key: true}
	found := map[string]bool{}
	stack := []*groupEntry{g}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		for _, m := range cur.members {
			if _, ok := users[m]; ok {
				found[m] = true
				continue
			}

			if nested, ok := groups[m]; ok && !visited[m] {
				stack = append(stack, nested)
				visited[m] = true
			}
		}
	}

	out := make([]string, 0, len(found))
	for k := range found {
		out = append(out, k)
	}

	sort.Strings(out)

	return out
}

// nearestOrgUnit returns the closest proper ancestor of dn that is a department.
func nearestOrgUnit(dn *goldap.DN, ous map[string]*ouEntry) *ouEntry {
	for i := 1; i < len(dn.RDNs); i++ {
		parent := &goldap.DN{RDNs: dn.RDNs[i:]}
		if ou, ok := ous[dnKey(parent)]; ok {
			return ou
		}
	}

	return nil
}

func parseDN(raw string) (*goldap.DN, string, error) {
	dn, err := goldap.ParseDN(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%w: parse DN %q: %w", ErrIncompleteRead, raw, err)
	}

	return dn, dnKey(dn), nil
}

// dnKey normalizes a DN for comparison: attribute types and values are
// compared case-insensitively, as for the usual naming attributes.
func dnKey(dn *goldap.DN) string {
	return strings.ToLower(dn.String())
}

func firstRDNValue(dn *goldap.DN) string {
	if len(dn.RDNs) == 0 || len(dn.RDNs[0].Attributes) == 0 {
		return ""
	}

	return dn.RDNs[0].Attributes[0].Value
}
