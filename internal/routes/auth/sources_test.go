package auth

import (
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/supabase-community/gotrue-go/types"

	v1 "github.com/neutree-ai/neutree/api/v1"
	internalauth "github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newTestLoginSources(t *testing.T, store storage.Storage) *LoginSources {
	t.Helper()

	sources, err := NewLoginSources(store, testJWTSecret, DefaultLoginSourceTTL)
	require.NoError(t, err)

	return sources
}

func ldapSource(name string, enabled bool) v1.IdentitySource {
	return v1.IdentitySource{
		ID:       1,
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.IdentitySourceSpec{
			Type:    v1.IdentitySourceTypeLDAP,
			Enabled: enabled,
			LDAP: &v1.IdentitySourceLDAPSpec{
				URL:        "ldap://ldap.example.org:389",
				BindDN:     "cn=svc,dc=example,dc=org",
				UserBaseDN: "ou=people,dc=example,dc=org",
				UserFilter: "(uid={username})",
			},
		},
	}
}

func matchSourceName(name string) interface{} {
	return mock.MatchedBy(func(option storage.ListOption) bool {
		return len(option.Filters) == 1 && option.Filters[0].Column == "metadata->name" &&
			option.Filters[0].Value == strconv.Quote(name)
	})
}

// expectSource makes the storage serve source and its secrets for any number
// of reads.
func expectSource(store *storagemocks.MockStorage, source v1.IdentitySource, secrets *storage.IdentitySourceSecrets) {
	store.EXPECT().ListIdentitySource(matchSourceName(source.Metadata.Name)).
		Return([]v1.IdentitySource{source}, nil).Maybe()
	store.EXPECT().GetIdentitySourceSecrets(source.Metadata.Name).Return(secrets, nil).Maybe()
}

// countingLDAP builds fakeLDAPs and counts them.
type countingLDAP struct {
	built   int
	configs []ldap.Config
}

func (c *countingLDAP) build(cfg ldap.Config) (LDAPAuthenticator, error) {
	c.built++
	c.configs = append(c.configs, cfg)

	return &fakeLDAP{identity: testIdentity()}, nil
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newCachingSources(t *testing.T, store storage.Storage) (*LoginSources, *countingLDAP, *clock) {
	t.Helper()

	sources := newTestLoginSources(t, store)
	builder := &countingLDAP{}
	clk := &clock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	sources.newLDAP = builder.build
	sources.now = clk.Now

	return sources, builder, clk
}

func TestLoginSources_BuildsFromSpecAndSecrets(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	source := ldapSource("corp-ldap", true)
	source.Spec.LDAP.Timeout = 7
	source.Spec.LDAP.CACert = "-----BEGIN CERTIFICATE-----\n..."
	source.Spec.LDAP.Attributes = &v1.IdentitySourceLDAPAttributes{MemberOf: "memberOf"}
	expectSource(store, source, &storage.IdentitySourceSecrets{LDAPBindPassword: "svc-secret"})

	sources, builder, _ := newCachingSources(t, store)

	entry, err := sources.resolve("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.NoError(t, err)
	assert.Equal(t, "corp-ldap", entry.name)

	require.Len(t, builder.configs, 1)
	cfg := builder.configs[0]
	assert.Equal(t, "svc-secret", cfg.BindPassword)
	assert.Equal(t, 7*time.Second, cfg.Timeout)
	assert.Equal(t, []byte(source.Spec.LDAP.CACert), cfg.RootCAs)
	assert.Equal(t, ldap.AttributeMapping{ID: "entryUUID", Username: "uid", Email: "mail", DisplayName: "cn", MemberOf: "memberOf"}, cfg.Attributes)
}

func TestLoginSources_CachesWithinTTL(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().ListIdentitySource(matchSourceName("corp-ldap")).
		Return([]v1.IdentitySource{ldapSource("corp-ldap", true)}, nil).Once()
	store.EXPECT().GetIdentitySourceSecrets("corp-ldap").
		Return(&storage.IdentitySourceSecrets{LDAPBindPassword: "svc"}, nil).Once()

	sources, builder, clk := newCachingSources(t, store)

	first, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.NoError(t, err)

	clk.now = clk.now.Add(DefaultLoginSourceTTL - time.Second)

	second, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.NoError(t, err)
	assert.Same(t, first, second)
	assert.Equal(t, 1, builder.built)
}

func TestLoginSources_RereadsAfterTTLAndKeepsUnchangedClient(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	expectSource(store, ldapSource("corp-ldap", true), &storage.IdentitySourceSecrets{LDAPBindPassword: "svc"})

	sources, builder, clk := newCachingSources(t, store)

	first, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.NoError(t, err)

	clk.now = clk.now.Add(DefaultLoginSourceTTL)

	second, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.NoError(t, err)

	store.AssertNumberOfCalls(t, "ListIdentitySource", 2)
	assert.Equal(t, 1, builder.built, "an unchanged source keeps its client")
	assert.Same(t, first.ldap, second.ldap)
}

func TestLoginSources_RebuildsOnChange(t *testing.T) {
	cases := map[string]func(source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets){
		"spec": func(source *v1.IdentitySource, _ *storage.IdentitySourceSecrets) {
			source.Spec.LDAP.UserFilter = "(cn={username})"
		},
		"secret": func(_ *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) {
			secrets.LDAPBindPassword = "rotated"
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			before := ldapSource("corp-ldap", true)
			after := ldapSource("corp-ldap", true)
			secretsAfter := &storage.IdentitySourceSecrets{LDAPBindPassword: "svc"}
			change(&after, secretsAfter)

			store.EXPECT().ListIdentitySource(mock.Anything).Return([]v1.IdentitySource{before}, nil).Once()
			store.EXPECT().GetIdentitySourceSecrets("corp-ldap").Return(&storage.IdentitySourceSecrets{LDAPBindPassword: "svc"}, nil).Once()
			store.EXPECT().ListIdentitySource(mock.Anything).Return([]v1.IdentitySource{after}, nil).Once()
			store.EXPECT().GetIdentitySourceSecrets("corp-ldap").Return(secretsAfter, nil).Once()

			sources, builder, clk := newCachingSources(t, store)

			_, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
			require.NoError(t, err)

			clk.now = clk.now.Add(DefaultLoginSourceTTL)

			_, err = sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
			require.NoError(t, err)
			assert.Equal(t, 2, builder.built)
			assert.Equal(t, secretsAfter.LDAPBindPassword, builder.configs[1].BindPassword)
		})
	}
}

func TestLoginSources_Unavailable(t *testing.T) {
	deleted := ldapSource("corp-ldap", true)
	deleted.Metadata.DeletionTimestamp = "2026-10-07T12:00:00Z"

	cases := []struct {
		name    string
		sources []v1.IdentitySource
		listErr error
		want    error
	}{
		{"missing", nil, nil, errSourceNotFound},
		{"disabled", []v1.IdentitySource{ldapSource("corp-ldap", false)}, nil, errSourceNotFound},
		{"being deleted", []v1.IdentitySource{deleted}, nil, errSourceNotFound},
		{"storage down", nil, errors.New("postgrest down"), nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			store.EXPECT().ListIdentitySource(mock.Anything).Return(tc.sources, tc.listErr).Once()

			sources, builder, _ := newCachingSources(t, store)

			_, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)

			require.Error(t, err)

			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			} else {
				assert.NotErrorIs(t, err, errSourceNotFound)
			}

			assert.Zero(t, builder.built)
		})
	}
}

// A source that was cached and is then disabled stops working on the first
// read after the TTL, and its entry is dropped.
func TestLoginSources_DisabledAfterCachingIsDropped(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.EXPECT().ListIdentitySource(mock.Anything).Return([]v1.IdentitySource{ldapSource("corp-ldap", true)}, nil).Once()
	store.EXPECT().GetIdentitySourceSecrets("corp-ldap").Return(&storage.IdentitySourceSecrets{LDAPBindPassword: "svc"}, nil).Once()
	store.EXPECT().ListIdentitySource(mock.Anything).Return([]v1.IdentitySource{ldapSource("corp-ldap", false)}, nil).Once()

	sources, _, clk := newCachingSources(t, store)

	_, err := sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.NoError(t, err)

	clk.now = clk.now.Add(DefaultLoginSourceTTL)

	_, err = sources.get("corp-ldap", v1.IdentitySourceTypeLDAP)
	require.ErrorIs(t, err, errSourceNotFound)

	sources.mu.Lock()
	defer sources.mu.Unlock()

	assert.NotContains(t, sources.entries, "corp-ldap")
}

func TestLoginSources_WrongTypeIsNotFound(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	expectSource(store, ldapSource("corp-ldap", true), &storage.IdentitySourceSecrets{LDAPBindPassword: "svc"})

	sources, _, _ := newCachingSources(t, store)

	_, err := sources.get("corp-ldap", v1.IdentitySourceTypeOIDC)
	assert.ErrorIs(t, err, errSourceNotFound)
}

func TestLoginSources_InvalidNameIsNotFoundWithoutReading(t *testing.T) {
	sources, _, _ := newCachingSources(t, storagemocks.NewMockStorage(t))

	for _, name := range []string{"Corp", "a.b", "../x", "corp_ldap"} {
		_, err := sources.get(name, v1.IdentitySourceTypeLDAP)
		assert.ErrorIs(t, err, errSourceNotFound, name)
	}
}

func TestLoginSources_DefaultSource(t *testing.T) {
	ldapA := v1.LoginIdentitySource{Name: "corp-ldap", Type: v1.IdentitySourceTypeLDAP}
	ldapB := v1.LoginIdentitySource{Name: "lab-ldap", Type: v1.IdentitySourceTypeLDAP}
	oidcA := v1.LoginIdentitySource{Name: "keycloak", Type: v1.IdentitySourceTypeOIDC}

	cases := []struct {
		name    string
		enabled []v1.LoginIdentitySource
		want    string
		wantErr error
	}{
		{"only one of the type", []v1.LoginIdentitySource{ldapA, oidcA}, "corp-ldap", nil},
		{"none of the type", []v1.LoginIdentitySource{oidcA}, "", errSourceNotFound},
		{"several of the type", []v1.LoginIdentitySource{ldapA, ldapB, oidcA}, "", errSourceRequired},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			store.EXPECT().ListLoginIdentitySources().Return(tc.enabled, nil).Once()

			sources, _, _ := newCachingSources(t, store)

			got, err := sources.defaultSource(v1.IdentitySourceTypeLDAP)

			assert.Equal(t, tc.want, got)

			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLDAPToken_SourceErrors(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		setup  func(store *storagemocks.MockStorage)
		status int
		msg    string
	}{
		{
			name: "unknown source",
			body: `{"source":"nope","username":"alice","password":"pw"}`,
			setup: func(store *storagemocks.MockStorage) {
				store.EXPECT().ListIdentitySource(matchSourceName("nope")).Return(nil, nil).Once()
			},
			status: http.StatusNotFound,
			msg:    msgSourceNotFound,
		},
		{
			name: "disabled source",
			body: `{"source":"lab-ldap","username":"alice","password":"pw"}`,
			setup: func(store *storagemocks.MockStorage) {
				store.EXPECT().ListIdentitySource(matchSourceName("lab-ldap")).
					Return([]v1.IdentitySource{ldapSource("lab-ldap", false)}, nil).Once()
			},
			status: http.StatusNotFound,
			msg:    msgSourceNotFound,
		},
		{
			name: "source omitted with two enabled",
			body: `{"username":"alice","password":"pw"}`,
			setup: func(store *storagemocks.MockStorage) {
				store.EXPECT().ListLoginIdentitySources().Return([]v1.LoginIdentitySource{
					{Name: "corp-ldap", Type: v1.IdentitySourceTypeLDAP},
					{Name: "lab-ldap", Type: v1.IdentitySourceTypeLDAP},
				}, nil).Once()
			},
			status: http.StatusBadRequest,
			msg:    "source is required: more than one LDAP identity source is enabled",
		},
		{
			name: "storage down",
			body: `{"source":"lab-ldap","username":"alice","password":"pw"}`,
			setup: func(store *storagemocks.MockStorage) {
				store.EXPECT().ListIdentitySource(matchSourceName("lab-ldap")).Return(nil, errors.New("postgrest down")).Once()
			},
			status: http.StatusServiceUnavailable,
			msg:    msgSourceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newLDAPTestDeps(t)
			tc.setup(d.storage)

			w := d.serve(t, true, http.MethodPost, "/api/v1/auth/ldap/token", tc.body)

			assert.Equal(t, tc.status, w.Code)
			assert.Equal(t, tc.msg, errorBody(t, w))
			d.client.AssertNotCalled(t, "AdminCreateUser", mock.Anything)
		})
	}
}

// With a single enabled LDAP source the client may leave the source out.
func TestLDAPToken_SourceOmittedWithOneEnabled(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.NewString()

	d.storage.EXPECT().ListLoginIdentitySources().Return([]v1.LoginIdentitySource{
		{Name: testLDAPSource, Type: v1.IdentitySourceTypeLDAP},
		{Name: "keycloak", Type: v1.IdentitySourceTypeOIDC},
	}, nil).Once()
	d.storage.EXPECT().GetExternalIdentity(testLDAPLinkSource, testIdentity().ExternalID).
		Return(&storage.ExternalIdentity{UserID: userID}, nil).Once()
	d.expectSession(userID)

	w := d.serve(t, true, http.MethodPost, "/api/v1/auth/ldap/token", `{"username":"alice","password":"pw"}`)

	assert.Equal(t, http.StatusOK, w.Code)
}

// Accounts of two LDAP sources are linked and named apart, even when the
// directories hand out the same ID.
func TestLDAPToken_LinksArePerSource(t *testing.T) {
	d := newLDAPTestDeps(t)
	userID := uuid.New()

	expectSource(d.storage, ldapSource("lab-ldap", true), &storage.IdentitySourceSecrets{LDAPBindPassword: "svc"})
	d.storage.EXPECT().GetExternalIdentity("ldap:lab-ldap", testIdentity().ExternalID).Return(nil, storage.ErrResourceNotFound).Once()
	d.client.EXPECT().AdminCreateUser(mock.MatchedBy(func(req types.AdminCreateUserRequest) bool {
		return req.Email == "8f6c0e5e-1b0b-4a4b-9c1d-2f3e4d5c6b7a@lab-ldap.ldap.neutree.local" &&
			req.AppMetadata["identity_source"] == "ldap"
	})).Return(&types.AdminCreateUserResponse{User: types.User{ID: userID}}, nil).Once()
	d.storage.EXPECT().CreateExternalIdentity(&storage.ExternalIdentity{
		Source: "ldap:lab-ldap", ExternalID: testIdentity().ExternalID, UserID: userID.String(),
		Username: "alice", Email: "alice@example.org",
	}).Return(nil).Once()
	d.expectEmail(userID.String(), "8f6c0e5e-1b0b-4a4b-9c1d-2f3e4d5c6b7a@lab-ldap.ldap.neutree.local")
	d.sessions.EXPECT().GenerateMagicLink(mock.Anything, "8f6c0e5e-1b0b-4a4b-9c1d-2f3e4d5c6b7a@lab-ldap.ldap.neutree.local").
		Return(&internalauth.MagicLink{UserID: userID.String(), HashedToken: "hash"}, nil).Once()
	d.sessions.EXPECT().VerifyMagicLink(mock.Anything, "hash").Return([]byte(testSession), nil).Once()

	w := d.serve(t, true, http.MethodPost, "/api/v1/auth/ldap/token", `{"source":"lab-ldap","username":"alice","password":"pw"}`)

	assert.Equal(t, http.StatusOK, w.Code)
}
