//go:build e2e

// E2E against a real OpenLDAP seeded with the neutree SSO fixture. Run with:
// LDAP_E2E_URL=ldap://host:389 LDAP_E2E_LDAPS_URL=ldaps://host:636 LDAP_E2E_CA_FILE=ca.crt LDAP_E2E_BIND_DN=... LDAP_E2E_BIND_PASSWORD=... LDAP_E2E_BASE_DN=ou=people,dc=... LDAP_E2E_USER_PASSWORD=... [LDAP_E2E_UNREACHABLE_URL=ldap://host:1] go test -tags e2e -count=1 -v ./pkg/identity/ldap/ -run E2E

package ldap

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/identity/orgsync"
)

type e2eEnv struct {
	url, ldapsURL, unreachableURL string
	ca                            []byte
	bindDN, bindPassword          string
	baseDN, userPassword          string
}

func loadE2EEnv(t *testing.T) e2eEnv {
	t.Helper()

	env := e2eEnv{
		url:            os.Getenv("LDAP_E2E_URL"),
		ldapsURL:       os.Getenv("LDAP_E2E_LDAPS_URL"),
		unreachableURL: os.Getenv("LDAP_E2E_UNREACHABLE_URL"),
		bindDN:         os.Getenv("LDAP_E2E_BIND_DN"),
		bindPassword:   os.Getenv("LDAP_E2E_BIND_PASSWORD"),
		baseDN:         os.Getenv("LDAP_E2E_BASE_DN"),
		userPassword:   os.Getenv("LDAP_E2E_USER_PASSWORD"),
	}

	caFile := os.Getenv("LDAP_E2E_CA_FILE")
	if env.url == "" || env.ldapsURL == "" || caFile == "" || env.bindDN == "" ||
		env.bindPassword == "" || env.baseDN == "" || env.userPassword == "" {
		t.Skip("LDAP_E2E_* env vars not set")
	}

	ca, err := os.ReadFile(caFile)
	require.NoError(t, err)

	env.ca = ca

	return env
}

func (e e2eEnv) config(rawURL string, startTLS bool, ca []byte) Config {
	return Config{
		URL:          rawURL,
		StartTLS:     startTLS,
		RootCAs:      ca,
		Timeout:      5 * time.Second,
		BindDN:       e.bindDN,
		BindPassword: e.bindPassword,
		UserBaseDN:   e.baseDN,
		UserFilter:   "(&(objectClass=inetOrgPerson)(uid={username}))",
		Attributes: AttributeMapping{
			ID:          "entryUUID",
			Username:    "uid",
			Email:       "mail",
			DisplayName: "cn",
			MemberOf:    "memberOf",
		},
	}
}

func e2eAuth(t *testing.T, cfg Config, username, password string) (*Identity, error) {
	t.Helper()

	a, err := New(cfg, NewDialer(cfg))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	return a.Authenticate(ctx, username, password)
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// groupDN returns the groups DN next to the people base, e.g. cn=admins,ou=groups,dc=neutree,dc=test.
func (e e2eEnv) groupDN(cn string) string {
	_, suffix, _ := strings.Cut(e.baseDN, ",")
	return "cn=" + cn + ",ou=groups," + suffix
}

func TestE2EAuthenticate(t *testing.T) {
	env := loadE2EEnv(t)

	ldaps := env.config(env.ldapsURL, false, env.ca)
	startTLS := env.config(env.url, true, env.ca)
	plain := env.config(env.url, false, nil)

	t.Run("ldaps with CA: alice", func(t *testing.T) {
		id, err := e2eAuth(t, ldaps, "alice", env.userPassword)
		require.NoError(t, err)
		assert.Regexp(t, uuidRE, id.ExternalID)
		assert.True(t, strings.HasPrefix(id.DN, "uid=alice,"), id.DN)
		assert.Equal(t, "alice", id.Username)
		assert.Equal(t, "alice@neutree.test", id.Email)
		assert.NotEmpty(t, id.DisplayName)
		assert.ElementsMatch(t, []string{env.groupDN("ai-taskforce"), env.groupDN("admins")}, id.Groups)
	})

	t.Run("starttls with CA: bob", func(t *testing.T) {
		id, err := e2eAuth(t, startTLS, "bob", env.userPassword)
		require.NoError(t, err)
		assert.Regexp(t, uuidRE, id.ExternalID)
		assert.True(t, strings.HasPrefix(id.DN, "uid=bob,"), id.DN)
		assert.Equal(t, "bob", id.Username)
		assert.Equal(t, "bob@neutree.test", id.Email)
		assert.NotEmpty(t, id.DisplayName)
		assert.Nil(t, id.Groups, "bob is in no group")
	})

	t.Run("plain ldap: carol", func(t *testing.T) {
		id, err := e2eAuth(t, plain, "carol", env.userPassword)
		require.NoError(t, err)
		assert.Equal(t, "carol", id.Username)
		assert.Equal(t, []string{env.groupDN("ai-taskforce")}, id.Groups)
	})

	t.Run("utf-8 display name", func(t *testing.T) {
		id, err := e2eAuth(t, ldaps, "zhang.san", env.userPassword)
		require.NoError(t, err)
		assert.Equal(t, "张三", id.DisplayName)
	})

	t.Run("wrong password", func(t *testing.T) {
		_, err := e2eAuth(t, ldaps, "alice", env.userPassword+"-wrong")
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	})

	t.Run("unknown user", func(t *testing.T) {
		_, err := e2eAuth(t, ldaps, "nobody-e2e", env.userPassword)
		assert.ErrorIs(t, err, ErrUserNotFound)
	})

	t.Run("duplicate uid", func(t *testing.T) {
		_, err := e2eAuth(t, ldaps, "dup", env.userPassword)
		assert.ErrorIs(t, err, ErrMultipleUsers)
	})

	for _, username := range []string{"alice)(uid=*", "*", "alice*", "*)(|(uid=*"} {
		t.Run("filter injection "+username, func(t *testing.T) {
			id, err := e2eAuth(t, ldaps, username, env.userPassword)
			assert.Nil(t, id)
			assert.ErrorIs(t, err, ErrUserNotFound)
		})
	}

	t.Run("empty password", func(t *testing.T) {
		_, err := e2eAuth(t, ldaps, "alice", "")
		assert.ErrorIs(t, err, ErrEmptyPassword)
		assert.ErrorIs(t, err, ErrInvalidCredentials)
	})

	t.Run("wrong service password", func(t *testing.T) {
		cfg := ldaps
		cfg.BindPassword += "-wrong"
		_, err := e2eAuth(t, cfg, "alice", env.userPassword)
		assert.ErrorIs(t, err, ErrServiceBindFailed)
	})

	t.Run("ldaps without CA", func(t *testing.T) {
		_, err := e2eAuth(t, env.config(env.ldapsURL, false, nil), "alice", env.userPassword)
		assert.ErrorIs(t, err, ErrTLS)
	})

	t.Run("starttls without CA", func(t *testing.T) {
		_, err := e2eAuth(t, env.config(env.url, true, nil), "alice", env.userPassword)
		assert.ErrorIs(t, err, ErrTLS)
	})

	t.Run("unreachable port", func(t *testing.T) {
		target := env.unreachableURL
		if target == "" {
			u, err := url.Parse(env.url)
			require.NoError(t, err)

			target = "ldap://" + u.Hostname() + ":1"
		}

		cfg := env.config(target, false, nil)
		cfg.Timeout = 2 * time.Second

		start := time.Now()
		_, err := e2eAuth(t, cfg, "alice", env.userPassword)
		assert.ErrorIs(t, err, ErrConnection)
		assert.Less(t, time.Since(start), 5*time.Second)
	})

	t.Run("external id stable across logins", func(t *testing.T) {
		first, err := e2eAuth(t, ldaps, "alice", env.userPassword)
		require.NoError(t, err)

		second, err := e2eAuth(t, startTLS, "alice", env.userPassword)
		require.NoError(t, err)
		assert.Equal(t, first.ExternalID, second.ExternalID)
		assert.Equal(t, first.DN, second.DN)
	})
}

// TestE2EDirectorySnapshot reads the org-test fixture: ou=org-test (rnd > ml > infer, market)
// with users ot.lin (infer), ot.chen (ml), ot.wang (market), ot.off (rnd, employeeType=disabled),
// and groups under ou=org-test-groups: ot-infra → ot-all → ot-core → ot-infra (a cycle) and
// ot-outer ⊃ ot-solo ⊃ ot.chen.
func TestE2EDirectorySnapshot(t *testing.T) {
	env := loadE2EEnv(t)

	_, suffix, _ := strings.Cut(env.baseDN, ",")
	cfg := env.config(env.ldapsURL, false, env.ca)
	cfg.UserBaseDN = "ou=org-test," + suffix
	cfg.Sync = SyncConfig{
		OrgUnitBaseDN:      "ou=org-test," + suffix,
		GroupBaseDN:        "ou=org-test-groups," + suffix,
		DisabledUserFilter: "(employeeType=disabled)",
		PageSize:           2, // forces several pages per search
	}

	d, err := NewDirectory(cfg, NewDialer(cfg))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snap, err := d.Snapshot(ctx)
	require.NoError(t, err)

	ouByName := map[string]string{} // display name → external ID
	parentOf := map[string]string{} // external ID → parent external ID

	for _, ou := range snap.OrgUnits {
		assert.Regexp(t, uuidRE, ou.ExternalID)
		ouByName[ou.DisplayName] = ou.ExternalID
		parentOf[ou.ExternalID] = ou.ParentExternalID
	}

	require.Len(t, snap.OrgUnits, 5)
	assert.Empty(t, parentOf[ouByName["org-test"]])
	assert.Equal(t, ouByName["org-test"], parentOf[ouByName["rnd"]])
	assert.Equal(t, ouByName["rnd"], parentOf[ouByName["ml"]])
	assert.Equal(t, ouByName["ml"], parentOf[ouByName["infer"]])
	assert.Equal(t, ouByName["org-test"], parentOf[ouByName["market"]])

	teamByName := map[string]string{}
	for _, team := range snap.Teams {
		teamByName[team.DisplayName] = team.ExternalID
	}

	require.Len(t, snap.Teams, 5)

	teams := func(names ...string) []string {
		ids := make([]string, 0, len(names))
		for _, n := range names {
			ids = append(ids, teamByName[n])
		}

		sort.Strings(ids)

		return ids
	}

	byUsername := map[string]orgsync.User{}
	for _, u := range snap.Users {
		byUsername[u.Username] = u
	}

	require.Len(t, snap.Users, 4)

	lin := byUsername["ot.lin"]
	assert.Equal(t, "林一", lin.DisplayName)
	assert.Equal(t, "ot.lin@neutree.test", lin.Email)
	assert.Equal(t, ouByName["infer"], lin.OrgUnitExternalID)
	assert.Equal(t, teams("ot-infra", "ot-core", "ot-all"), lin.TeamExternalIDs, "cycle expanded")
	assert.False(t, lin.Disabled)

	assert.Equal(t, ouByName["market"], byUsername["ot.wang"].OrgUnitExternalID)
	assert.Equal(t, teams("ot-infra", "ot-core", "ot-all"), byUsername["ot.wang"].TeamExternalIDs)

	assert.Equal(t, ouByName["ml"], byUsername["ot.chen"].OrgUnitExternalID)
	assert.Equal(t, teams("ot-solo", "ot-outer"), byUsername["ot.chen"].TeamExternalIDs, "nested group expanded")

	off := byUsername["ot.off"]
	assert.True(t, off.Disabled)
	assert.Equal(t, ouByName["rnd"], off.OrgUnitExternalID)
	assert.Equal(t, teams("ot-infra", "ot-core", "ot-all"), off.TeamExternalIDs)

	plan, err := orgsync.Diff(snap, nil)
	require.NoError(t, err)
	assert.Len(t, plan.OrgUnits, 5)
	assert.Len(t, plan.Users, 3, "the disabled user is not created")

	again, err := d.Snapshot(ctx)
	require.NoError(t, err)
	assert.Equal(t, snap, again, "a re-read is identical")
}
