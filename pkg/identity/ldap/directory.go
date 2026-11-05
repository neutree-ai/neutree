package ldap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

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
	// DisabledUserFilter selects the disabled users among those; empty means
	// none. OpenLDAP has no standard flag, e.g. "(pwdAccountLockedTime=*)"
	// with ppolicy; for Active Directory use ADDisabledUserFilter.
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

// NewDirectory validates cfg and sync and returns a Directory using dial to
// reach the directory.
func NewDirectory(cfg Config, sync SyncConfig, dial Dialer) (*Directory, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if dial == nil {
		return nil, fmt.Errorf("%w: dialer is required", ErrInvalidConfig)
	}

	sc := sync.withDefaults(cfg.UserFilter)
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
	conn, release, err := d.cfg.connectAndBind(ctx, d.dial)
	if err != nil {
		return nil, err
	}
	defer release()

	r := &reader{d: d, ctx: ctx, conn: conn}

	return r.read()
}

type reader struct {
	d    *Directory
	ctx  context.Context
	conn goldap.Client
}

// entryID is a parsed entry: its DN, the normalized DN (dnKey) and its external ID.
type entryID struct {
	dn  *goldap.DN
	key string
	id  string
}

type ouEntry struct {
	entryID
	unit orgsync.OrgUnit
}

type groupEntry struct {
	entryID
	team    orgsync.Team
	members []string // normalized member DNs
}

type userEntry struct {
	entryID
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

	ouByKey := byKey(ous, func(e *ouEntry) string { return e.key })
	groupByKey := byKey(groups, func(e *groupEntry) string { return e.key })
	userByKey := byKey(users, func(e *userEntry) string { return e.key })

	snap := &orgsync.Snapshot{
		OrgUnits: make([]orgsync.OrgUnit, 0, len(ous)),
		Teams:    make([]orgsync.Team, 0, len(groups)),
		Users:    make([]orgsync.User, 0, len(users)),
	}

	for _, ou := range ous {
		if parent := nearestOrgUnit(ou.entryID, ouByKey); parent != nil {
			ou.unit.ParentExternalID = parent.unit.ExternalID
		}

		snap.OrgUnits = append(snap.OrgUnits, ou.unit)
	}

	teamsOf := make(map[string][]string, len(users))

	for _, g := range groups {
		snap.Teams = append(snap.Teams, g.team)

		for _, userKey := range expandMembers(g, groupByKey, userByKey) {
			teamsOf[userKey] = append(teamsOf[userKey], g.team.ExternalID)
		}
	}

	for _, u := range users {
		if ou := nearestOrgUnit(u.entryID, ouByKey); ou != nil {
			u.user.OrgUnitExternalID = ou.unit.ExternalID
		}

		teams := teamsOf[u.key]
		slices.Sort(teams)
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
		ent, err := r.identify(e, seen)
		if err != nil {
			return nil, err
		}

		out = append(out, &ouEntry{entryID: ent, unit: orgsync.OrgUnit{
			ExternalID: ent.id, DisplayName: nameOf(e, sc.OrgUnitNameAttribute, ent.dn), DN: e.DN,
		}})
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
	// The same member DN usually appears in many groups.
	memberKeys := map[string]string{}

	for _, e := range entries {
		ent, err := r.identify(e, seen)
		if err != nil {
			return nil, err
		}

		values, err := r.memberValues(e)
		if err != nil {
			return nil, err
		}

		members := make([]string, 0, len(values))

		for _, v := range values {
			memberKey, ok := memberKeys[v]
			if !ok {
				_, memberKey, err = parseDN(v)
				if err != nil {
					return nil, err
				}

				memberKeys[v] = memberKey
			}

			members = append(members, memberKey)
		}

		out = append(out, &groupEntry{
			entryID: ent,
			team:    orgsync.Team{ExternalID: ent.id, DisplayName: nameOf(e, sc.GroupNameAttribute, ent.dn), DN: e.DN},
			members: members,
		})
	}

	return out, nil
}

// memberValues returns all member DNs of a group entry. Active Directory
// returns a large attribute in ranges ("member;range=0-1499"); the remaining
// ranges are fetched with base searches on the group.
func (r *reader) memberValues(e *goldap.Entry) ([]string, error) {
	attr := r.d.sync.GroupMemberAttribute
	values := e.GetEqualFoldAttributeValues(attr)

	more, low, high, ranged, err := rangedValues(e, attr)
	if err != nil || !ranged {
		return values, err
	}

	values = append(values, more...)

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
			return nil, operationError(r.ctx, nil, "read group members", err)
		}

		if len(res.Entries) != 1 {
			return nil, fmt.Errorf("%w: group %q: range read returned %d entries", ErrIncompleteRead, e.DN, len(res.Entries))
		}

		var ok bool

		more, _, high, ok, err = rangedValues(res.Entries[0], attr)
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, fmt.Errorf("%w: group %q: range read returned no range", ErrIncompleteRead, e.DN)
		}

		values = append(values, more...)
	}

	return values, nil
}

// rangedValues returns the values of "<attr>;range=<low>-<high>", with low and
// high ("*" for the last range). ranged is false when the entry has no ranged
// attribute.
func rangedValues(e *goldap.Entry, attr string) (values []string, low int, high string, ranged bool, err error) {
	prefix := strings.ToLower(attr) + ";range="

	for _, a := range e.Attributes {
		name := strings.ToLower(a.Name)
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		lowStr, highStr, ok := strings.Cut(strings.TrimPrefix(name, prefix), "-")
		if !ok {
			return nil, 0, "", false, fmt.Errorf("%w: entry %q: bad range attribute %q", ErrIncompleteRead, e.DN, a.Name)
		}

		low, err = strconv.Atoi(lowStr)
		if err != nil {
			return nil, 0, "", false, fmt.Errorf("%w: entry %q: bad range attribute %q", ErrIncompleteRead, e.DN, a.Name)
		}

		return a.Values, low, highStr, true, nil
	}

	return nil, 0, "", false, nil
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
		ent, err := r.identify(e, seen)
		if err != nil {
			return nil, err
		}

		identity, err := m.mapEntry(e)
		if err != nil {
			return nil, fmt.Errorf("%w (entry %q)", err, e.DN)
		}

		out = append(out, &userEntry{entryID: ent, user: orgsync.User{
			ExternalID:  identity.ExternalID,
			Username:    identity.Username,
			Email:       identity.Email,
			DisplayName: identity.DisplayName,
			DN:          e.DN,
		}})
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
			return nil, operationError(r.ctx, nil, op, err)
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
	return uniqueAttributes(append([]string{r.d.cfg.Attributes.ID}, extra...)...)
}

// identify parses the DN of e and reads its external ID, which must not be in seen yet.
func (r *reader) identify(e *goldap.Entry, seen map[string]bool) (entryID, error) {
	dn, key, err := parseDN(e.DN)
	if err != nil {
		return entryID{}, err
	}

	id, err := externalID(e, r.d.cfg.Attributes.ID)
	if err != nil {
		return entryID{}, fmt.Errorf("%w (entry %q)", err, e.DN)
	}

	if seen[id] {
		return entryID{}, fmt.Errorf("%w: duplicate %s %q", ErrIncompleteRead, r.d.cfg.Attributes.ID, id)
	}

	seen[id] = true

	return entryID{dn: dn, key: key, id: id}, nil
}

// nameOf returns the name attribute of e, or else the value of the first RDN.
func nameOf(e *goldap.Entry, attr string, dn *goldap.DN) string {
	if name := e.GetEqualFoldAttributeValue(attr); name != "" {
		return name
	}

	if len(dn.RDNs) == 0 || len(dn.RDNs[0].Attributes) == 0 {
		return ""
	}

	return dn.RDNs[0].Attributes[0].Value
}

func byKey[T any](items []T, key func(T) string) map[string]T {
	out := make(map[string]T, len(items))
	for _, item := range items {
		out[key(item)] = item
	}

	return out
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

	return slices.Collect(maps.Keys(found))
}

// nearestOrgUnit returns the closest proper ancestor of e that is a department.
func nearestOrgUnit(e entryID, ous map[string]*ouEntry) *ouEntry {
	if len(ous) == 0 {
		return nil
	}

	// An ancestor's key is a suffix of e.key: dnKey joins the lowered RDNs with ",".
	offset := 0

	for _, rdn := range e.dn.RDNs[:max(len(e.dn.RDNs)-1, 0)] {
		offset += len(strings.ToLower(rdn.String())) + 1
		if ou, ok := ous[e.key[offset:]]; ok {
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
