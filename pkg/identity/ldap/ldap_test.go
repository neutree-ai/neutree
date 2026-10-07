package ldap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"go/build"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	serviceDN       = "cn=svc,dc=example,dc=org"
	servicePassword = "svc-secret"
	userDN          = "uid=alice,ou=people,dc=example,dc=org"
	userPassword    = "alice-secret"
)

// fakeConn implements the parts of goldap.Client the Authenticator uses. Any other method
// hits the nil embedded interface and panics.
type fakeConn struct {
	goldap.Client

	serviceBindErr error
	userBindErr    error
	searchResult   *goldap.SearchResult
	searchErr      error

	binds    []string
	searches []*goldap.SearchRequest
	closed   int
}

func (f *fakeConn) Bind(dn, password string) error {
	f.binds = append(f.binds, dn)

	if dn == serviceDN {
		if password != servicePassword {
			return goldap.NewError(goldap.LDAPResultInvalidCredentials, errors.New("bad service password"))
		}

		return f.serviceBindErr
	}

	return f.userBindErr
}

func (f *fakeConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	f.searches = append(f.searches, req)

	if f.searchResult == nil {
		return &goldap.SearchResult{}, f.searchErr
	}

	return f.searchResult, f.searchErr
}

func (f *fakeConn) Close() error {
	f.closed++
	return nil
}

func openLDAPConfig() Config {
	return Config{
		URL:          "ldap://ldap.example.org:389",
		BindDN:       serviceDN,
		BindPassword: servicePassword,
		UserBaseDN:   "ou=people,dc=example,dc=org",
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

func adConfig() Config {
	cfg := openLDAPConfig()
	cfg.UserFilter = "(&(objectClass=user)(sAMAccountName={username}))"
	cfg.Attributes = AttributeMapping{
		ID:          "objectGUID",
		Username:    "sAMAccountName",
		Email:       "mail",
		DisplayName: "displayName",
		MemberOf:    "memberOf",
	}

	return cfg
}

func result(entries ...*goldap.Entry) *goldap.SearchResult {
	return &goldap.SearchResult{Entries: entries}
}

// adGUID is the MS-DTYP example GUID {6F9619FF-8B86-D011-B42D-00C04FC964FF} in wire byte order.
var adGUID = string([]byte{0xFF, 0x19, 0x96, 0x6F, 0x86, 0x8B, 0x11, 0xD0, 0xB4, 0x2D, 0x00, 0xC0, 0x4F, 0xC9, 0x64, 0xFF})

func TestAuthenticate(t *testing.T) {
	aliceEntry := goldap.NewEntry(userDN, map[string][]string{
		"entryUUID": {"5b3a5f3e-1c2d-4e5f-8a9b-0c1d2e3f4a5b"},
		"uid":       {"alice"},
		"mail":      {"alice@example.org"},
		"cn":        {"Alice Liddell"},
		"memberOf":  {"cn=dev,ou=groups,dc=example,dc=org", "cn=ops,ou=groups,dc=example,dc=org"},
	})
	tlsErr := goldap.NewError(goldap.ErrorNetwork, x509.UnknownAuthorityError{})

	cases := []struct {
		name     string
		cfg      Config
		username string
		password string
		conn     *fakeConn
		dialErr  error

		wantErr      []error
		wantIdentity *Identity
		wantBinds    []string
		wantFilter   string
	}{
		{
			name:     "openldap happy path",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			conn:     &fakeConn{searchResult: result(aliceEntry)},
			wantIdentity: &Identity{
				ExternalID:  "5b3a5f3e-1c2d-4e5f-8a9b-0c1d2e3f4a5b",
				DN:          userDN,
				Username:    "alice",
				Email:       "alice@example.org",
				DisplayName: "Alice Liddell",
				Groups:      []string{"cn=dev,ou=groups,dc=example,dc=org", "cn=ops,ou=groups,dc=example,dc=org"},
			},
			wantBinds:  []string{serviceDN, userDN},
			wantFilter: "(&(objectClass=inetOrgPerson)(uid=alice))",
		},
		{
			name:     "active directory objectGUID",
			cfg:      adConfig(),
			username: "alice",
			password: userPassword,
			conn: &fakeConn{searchResult: result(goldap.NewEntry(userDN, map[string][]string{
				"objectGUID":     {adGUID},
				"sAMAccountName": {"alice"},
				"mail":           {"alice@corp.example"},
				"displayName":    {"Alice L."},
			}))},
			wantIdentity: &Identity{
				ExternalID:  "6f9619ff-8b86-d011-b42d-00c04fc964ff",
				DN:          userDN,
				Username:    "alice",
				Email:       "alice@corp.example",
				DisplayName: "Alice L.",
			},
			wantBinds:  []string{serviceDN, userDN},
			wantFilter: "(&(objectClass=user)(sAMAccountName=alice))",
		},
		{
			name:     "attribute names match case-insensitively",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			conn: &fakeConn{searchResult: result(goldap.NewEntry(userDN, map[string][]string{
				"entryuuid": {"id-1"},
				"UID":       {"alice"},
			}))},
			wantIdentity: &Identity{ExternalID: "id-1", DN: userDN, Username: "alice"},
			wantBinds:    []string{serviceDN, userDN},
		},
		{
			name:     "missing optional attributes and no groups",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			conn: &fakeConn{searchResult: result(goldap.NewEntry(userDN, map[string][]string{
				"entryUUID": {"id-1"},
				"uid":       {"alice"},
			}))},
			wantIdentity: &Identity{ExternalID: "id-1", DN: userDN, Username: "alice"},
			wantBinds:    []string{serviceDN, userDN},
		},
		{
			name: "memberOf mapping disabled",
			cfg: func() Config {
				cfg := openLDAPConfig()
				cfg.Attributes.MemberOf = ""
				return cfg
			}(),
			username: "alice",
			password: userPassword,
			conn:     &fakeConn{searchResult: result(aliceEntry)},
			wantIdentity: &Identity{
				ExternalID:  "5b3a5f3e-1c2d-4e5f-8a9b-0c1d2e3f4a5b",
				DN:          userDN,
				Username:    "alice",
				Email:       "alice@example.org",
				DisplayName: "Alice Liddell",
			},
			wantBinds: []string{serviceDN, userDN},
		},
		{
			name:       "filter escapes username",
			cfg:        openLDAPConfig(),
			username:   "a*)(uid=*",
			password:   userPassword,
			conn:       &fakeConn{},
			wantErr:    []error{ErrUserNotFound},
			wantBinds:  []string{serviceDN},
			wantFilter: `(&(objectClass=inetOrgPerson)(uid=a\2a\29\28uid=\2a))`,
		},
		{
			name:     "empty password rejected before dial",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: "",
			wantErr:  []error{ErrEmptyPassword, ErrInvalidCredentials},
		},
		{
			name:     "empty username rejected before dial",
			cfg:      openLDAPConfig(),
			username: "  ",
			password: userPassword,
			wantErr:  []error{ErrEmptyUsername, ErrInvalidCredentials},
		},
		{
			name:      "user not found",
			cfg:       openLDAPConfig(),
			username:  "nobody",
			password:  userPassword,
			conn:      &fakeConn{searchResult: result()},
			wantErr:   []error{ErrUserNotFound},
			wantBinds: []string{serviceDN},
		},
		{
			name:      "two entries returned",
			cfg:       openLDAPConfig(),
			username:  "alice",
			password:  userPassword,
			conn:      &fakeConn{searchResult: result(aliceEntry, aliceEntry)},
			wantErr:   []error{ErrMultipleUsers},
			wantBinds: []string{serviceDN},
		},
		{
			name:     "server size limit exceeded with two entries",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			conn: &fakeConn{
				searchResult: result(aliceEntry, aliceEntry),
				searchErr:    goldap.NewError(goldap.LDAPResultSizeLimitExceeded, errors.New("size limit exceeded")),
			},
			wantErr:   []error{ErrMultipleUsers},
			wantBinds: []string{serviceDN},
		},
		{
			name:      "service bind rejected",
			cfg:       openLDAPConfig(),
			username:  "alice",
			password:  userPassword,
			conn:      &fakeConn{serviceBindErr: goldap.NewError(goldap.LDAPResultInvalidCredentials, errors.New("bad"))},
			wantErr:   []error{ErrServiceBindFailed},
			wantBinds: []string{serviceDN},
		},
		{
			name:      "user bind invalid credentials",
			cfg:       openLDAPConfig(),
			username:  "alice",
			password:  "wrong",
			conn:      &fakeConn{searchResult: result(aliceEntry), userBindErr: goldap.NewError(goldap.LDAPResultInvalidCredentials, errors.New("bad"))},
			wantErr:   []error{ErrInvalidCredentials},
			wantBinds: []string{serviceDN, userDN},
		},
		{
			name:      "missing ID attribute",
			cfg:       openLDAPConfig(),
			username:  "alice",
			password:  userPassword,
			conn:      &fakeConn{searchResult: result(goldap.NewEntry(userDN, map[string][]string{"uid": {"alice"}}))},
			wantErr:   []error{ErrMissingAttribute},
			wantBinds: []string{serviceDN, userDN},
		},
		{
			name:      "malformed objectGUID",
			cfg:       adConfig(),
			username:  "alice",
			password:  userPassword,
			conn:      &fakeConn{searchResult: result(goldap.NewEntry(userDN, map[string][]string{"objectGUID": {"short"}, "sAMAccountName": {"alice"}}))},
			wantErr:   []error{ErrMissingAttribute},
			wantBinds: []string{serviceDN, userDN},
		},
		{
			name:     "dial network error",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			dialErr:  goldap.NewError(goldap.ErrorNetwork, &net.OpError{Op: "dial", Err: errors.New("connection refused")}),
			wantErr:  []error{ErrConnection},
		},
		{
			name:     "dial TLS error",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			dialErr:  tlsErr,
			wantErr:  []error{ErrTLS},
		},
		{
			name:     "dial certificate verification error",
			cfg:      openLDAPConfig(),
			username: "alice",
			password: userPassword,
			dialErr:  &tls.CertificateVerificationError{Err: x509.HostnameError{Host: "ldap.example.org"}},
			wantErr:  []error{ErrTLS},
		},
		{
			name:      "network error during service bind",
			cfg:       openLDAPConfig(),
			username:  "alice",
			password:  userPassword,
			conn:      &fakeConn{serviceBindErr: goldap.NewError(goldap.ErrorNetwork, errors.New("connection closed"))},
			wantErr:   []error{ErrConnection},
			wantBinds: []string{serviceDN},
		},
		{
			name:      "network error during search",
			cfg:       openLDAPConfig(),
			username:  "alice",
			password:  userPassword,
			conn:      &fakeConn{searchErr: goldap.NewError(goldap.ErrorNetwork, errors.New("connection timed out"))},
			wantErr:   []error{ErrConnection},
			wantBinds: []string{serviceDN},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dialed := 0
			dial := func(context.Context) (goldap.Client, error) {
				dialed++
				if tc.dialErr != nil {
					return nil, tc.dialErr
				}

				return tc.conn, nil
			}

			auth, err := New(tc.cfg, dial)
			require.NoError(t, err)

			identity, err := auth.Authenticate(context.Background(), tc.username, tc.password)

			if len(tc.wantErr) > 0 {
				require.Error(t, err)
				assert.Nil(t, identity)

				for _, want := range tc.wantErr {
					assert.ErrorIs(t, err, want)
				}

				if tc.password != "" {
					assert.NotContains(t, err.Error(), tc.password, "password must not leak")
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.wantIdentity, identity)
			}

			if tc.conn == nil && tc.dialErr == nil {
				assert.Zero(t, dialed, "must not dial")
				return
			}

			assert.Equal(t, 1, dialed)

			if tc.conn == nil {
				return
			}

			assert.Equal(t, 1, tc.conn.closed, "connection must be closed exactly once")
			assert.Equal(t, tc.wantBinds, tc.conn.binds)

			if len(tc.conn.searches) > 0 {
				req := tc.conn.searches[0]
				assert.Equal(t, tc.cfg.UserBaseDN, req.BaseDN)
				assert.Equal(t, goldap.ScopeWholeSubtree, req.Scope)
				assert.Equal(t, 2, req.SizeLimit)

				if tc.wantFilter != "" {
					assert.Equal(t, tc.wantFilter, req.Filter)
				}
			}
		})
	}
}

func TestAuthenticateRequestsOnlyMappedAttributes(t *testing.T) {
	cfg := openLDAPConfig()
	cfg.Attributes.Email = ""
	cfg.Attributes.DisplayName = "UID"
	conn := &fakeConn{searchResult: result()}

	auth, err := New(cfg, func(context.Context) (goldap.Client, error) { return conn, nil })
	require.NoError(t, err)

	_, err = auth.Authenticate(context.Background(), "alice", userPassword)
	require.ErrorIs(t, err, ErrUserNotFound)
	require.Len(t, conn.searches, 1)
	assert.Equal(t, []string{"entryUUID", "uid", "memberOf"}, conn.searches[0].Attributes)
}

func TestAuthenticateCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn := &fakeConn{serviceBindErr: goldap.NewError(goldap.ErrorNetwork, errors.New("connection closed"))}
	auth, err := New(openLDAPConfig(), func(context.Context) (goldap.Client, error) { return conn, nil })
	require.NoError(t, err)

	_, err = auth.Authenticate(ctx, "alice", userPassword)
	require.ErrorIs(t, err, ErrConnection)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, conn.closed)
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		ok     bool
	}{
		{name: "valid ldap", mutate: func(*Config) {}, ok: true},
		{name: "valid ldap with StartTLS", mutate: func(c *Config) { c.StartTLS = true }, ok: true},
		{name: "valid ldaps", mutate: func(c *Config) { c.URL = "ldaps://ldap.example.org:636" }, ok: true},
		{name: "ldaps with StartTLS", mutate: func(c *Config) { c.URL = "ldaps://ldap.example.org"; c.StartTLS = true }},
		{name: "bad scheme", mutate: func(c *Config) { c.URL = "http://ldap.example.org" }},
		{name: "no host", mutate: func(c *Config) { c.URL = "ldap://" }},
		{name: "negative timeout", mutate: func(c *Config) { c.Timeout = -1 }},
		{name: "no bind DN", mutate: func(c *Config) { c.BindDN = "" }},
		{name: "no bind password", mutate: func(c *Config) { c.BindPassword = "" }},
		{name: "no base DN", mutate: func(c *Config) { c.UserBaseDN = "" }},
		{name: "filter without placeholder", mutate: func(c *Config) { c.UserFilter = "(uid=alice)" }},
		{name: "no ID attribute", mutate: func(c *Config) { c.Attributes.ID = "" }},
		{name: "no username attribute", mutate: func(c *Config) { c.Attributes.Username = "" }},
		{name: "invalid root CAs", mutate: func(c *Config) { c.RootCAs = []byte("not a pem") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := openLDAPConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if tc.ok {
				require.NoError(t, err)
				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.NotContains(t, err.Error(), servicePassword)
		})
	}

	_, err := New(openLDAPConfig(), nil)
	require.ErrorIs(t, err, ErrInvalidConfig)
}

// TestNewDialer exercises the real dialer against local listeners: a closed port and a TLS
// server whose certificate is or is not trusted via RootCAs. No LDAP traffic is exchanged.
func TestNewDialer(t *testing.T) {
	tlsServer := httptest.NewTLSServer(nil)
	t.Cleanup(tlsServer.Close)

	tlsAddr := tlsServer.Listener.Addr().String()
	serverCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw})

	closed, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	closedAddr := closed.Addr().String()
	require.NoError(t, closed.Close())

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{name: "connection refused", mutate: func(c *Config) { c.URL = "ldap://" + closedAddr }, wantErr: ErrConnection},
		{name: "untrusted certificate", mutate: func(c *Config) { c.URL = "ldaps://" + tlsAddr }, wantErr: ErrTLS},
		{name: "trusted via RootCAs", mutate: func(c *Config) { c.URL = "ldaps://" + tlsAddr; c.RootCAs = serverCA }},
		{name: "insecure skip verify", mutate: func(c *Config) { c.URL = "ldaps://" + tlsAddr; c.InsecureSkipVerify = true }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := openLDAPConfig()
			tc.mutate(&cfg)

			conn, err := NewDialer(cfg)(context.Background())
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			require.NoError(t, conn.Close())
		})
	}
}

func TestFormatGUID(t *testing.T) {
	got, err := formatGUID([]byte(adGUID))
	require.NoError(t, err)
	assert.Equal(t, "6f9619ff-8b86-d011-b42d-00c04fc964ff", got)

	_, err = formatGUID([]byte{1, 2, 3})
	require.ErrorIs(t, err, ErrMissingAttribute)
}

// TestNoNeutreeImports keeps the package reusable outside neutree: only the standard
// library and go-ldap may be imported by non-test files.
func TestNoNeutreeImports(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)

	for _, path := range pkg.Imports {
		firstElem := strings.SplitN(path, "/", 2)[0]
		if !strings.Contains(firstElem, ".") {
			continue // standard library
		}

		assert.True(t, strings.HasPrefix(path, "github.com/go-ldap/ldap/"), "unexpected import %q", path)
	}
}

func TestPing(t *testing.T) {
	cases := []struct {
		name      string
		cfg       func() Config
		conn      *fakeConn
		dialErr   error
		wantErr   error
		wantBinds []string
	}{
		{
			name:      "service account binds",
			cfg:       openLDAPConfig,
			conn:      &fakeConn{},
			wantBinds: []string{serviceDN},
		},
		{
			name: "wrong service password",
			cfg: func() Config {
				cfg := openLDAPConfig()
				cfg.BindPassword = "wrong"

				return cfg
			},
			conn:      &fakeConn{},
			wantErr:   ErrServiceBindFailed,
			wantBinds: []string{serviceDN},
		},
		{
			name:    "dial fails",
			cfg:     openLDAPConfig,
			dialErr: &net.OpError{Op: "dial", Err: errors.New("connection refused")},
			wantErr: ErrConnection,
		},
		{
			name:    "untrusted certificate",
			cfg:     openLDAPConfig,
			dialErr: goldap.NewError(goldap.ErrorNetwork, x509.UnknownAuthorityError{}),
			wantErr: ErrTLS,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := New(tc.cfg(), func(context.Context) (goldap.Client, error) {
				if tc.dialErr != nil {
					return nil, tc.dialErr
				}

				return tc.conn, nil
			})
			require.NoError(t, err)

			err = auth.Ping(context.Background())

			if tc.wantErr == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.wantErr)
				assert.NotContains(t, err.Error(), servicePassword)
			}

			if tc.conn != nil {
				assert.Equal(t, tc.wantBinds, tc.conn.binds)
				assert.Empty(t, tc.conn.searches, "a ping searches nothing")
				assert.Equal(t, 1, tc.conn.closed)
			}
		})
	}
}
