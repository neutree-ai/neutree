package identitysource

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
	"github.com/neutree-ai/neutree/pkg/identity/oidc/oidctest"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func ldapSpec(url string) *v1.IdentitySourceSpec {
	return &v1.IdentitySourceSpec{
		Type:    v1.IdentitySourceTypeLDAP,
		Enabled: true,
		LDAP: &v1.IdentitySourceLDAPSpec{
			URL:        url,
			Timeout:    2,
			BindDN:     "cn=svc,dc=example,dc=org",
			UserBaseDN: "ou=people,dc=example,dc=org",
			UserFilter: "(uid={username})",
		},
	}
}

func oidcSpec(idp *oidctest.Provider) *v1.IdentitySourceSpec {
	return &v1.IdentitySourceSpec{
		Type:    v1.IdentitySourceTypeOIDC,
		Enabled: true,
		OIDC: &v1.IdentitySourceOIDCSpec{
			Issuer:           idp.Issuer(),
			ClientID:         "neutree",
			CACert:           string(idp.CAPEM()),
			RedirectURL:      "https://neutree.example.org/api/v1/auth/oidc/callback",
			AllowedRedirects: []string{"https://neutree.example.org/"},
		},
	}
}

func TestLDAPConfig(t *testing.T) {
	spec := ldapSpec("ldaps://ldap.example.org:636").LDAP
	spec.CACert = "-----BEGIN CERTIFICATE-----"
	spec.Attributes = &v1.IdentitySourceLDAPAttributes{ID: "objectGUID", Username: " ", MemberOf: "memberOf"}

	cfg := LDAPConfig(spec, "pw")

	assert.Equal(t, "pw", cfg.BindPassword)
	assert.Equal(t, 2*time.Second, cfg.Timeout)
	assert.Equal(t, []byte(spec.CACert), cfg.RootCAs)
	assert.Equal(t, ldap.AttributeMapping{ID: "objectGUID", Username: "uid", Email: "mail", DisplayName: "cn", MemberOf: "memberOf"}, cfg.Attributes)

	cfg.RootCAs = nil
	assert.NoError(t, cfg.Validate())
}

func TestOIDCConfig_Defaults(t *testing.T) {
	cfg := OIDCConfig(&v1.IdentitySourceOIDCSpec{
		Issuer:      "https://idp.example.org",
		ClientID:    "neutree",
		RedirectURL: "https://neutree.example.org/cb",
	}, "secret")

	assert.Equal(t, "secret", cfg.ClientSecret)
	assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Scopes)
	assert.Nil(t, cfg.RootCAs)
	assert.Equal(t, oidc.ClaimMapping{Username: "preferred_username", DisplayName: "name", Email: "email"}, cfg.Claims)
	assert.NoError(t, cfg.Validate())
}

func TestFingerprint(t *testing.T) {
	spec := ldapSpec("ldap://a:389")
	secrets := &storage.IdentitySourceSecrets{LDAPBindPassword: "pw"}
	base := Fingerprint(spec, secrets)

	assert.Equal(t, base, Fingerprint(ldapSpec("ldap://a:389"), &storage.IdentitySourceSecrets{LDAPBindPassword: "pw"}))
	assert.NotEqual(t, base, Fingerprint(ldapSpec("ldap://b:389"), secrets), "spec change")
	assert.NotEqual(t, base, Fingerprint(spec, &storage.IdentitySourceSecrets{LDAPBindPassword: "pw2"}), "secret change")
}

func TestConnectionTest_OIDC(t *testing.T) {
	idp := oidctest.New(t, "neutree", "secret")

	err := ConnectionTest(context.Background(), &v1.IdentitySource{Spec: oidcSpec(idp)}, &storage.IdentitySourceSecrets{OIDCClientSecret: "secret"})
	assert.NoError(t, err)

	untrusted := oidcSpec(idp)
	untrusted.OIDC.CACert = ""
	err = ConnectionTest(context.Background(), &v1.IdentitySource{Spec: untrusted}, nil)
	assert.ErrorIs(t, err, oidc.ErrDiscovery)
}

func TestConnectionTest_LDAPUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addr := listener.Addr().String()
	require.NoError(t, listener.Close())

	err = ConnectionTest(context.Background(), &v1.IdentitySource{Spec: ldapSpec("ldap://" + addr)},
		&storage.IdentitySourceSecrets{LDAPBindPassword: "pw"})
	assert.ErrorIs(t, err, ldap.ErrConnection)
}

func TestConnectionTest_Invalid(t *testing.T) {
	cases := map[string]*v1.IdentitySource{
		"no spec":      {},
		"no ldap spec": {Spec: &v1.IdentitySourceSpec{Type: v1.IdentitySourceTypeLDAP}},
		"no oidc spec": {Spec: &v1.IdentitySourceSpec{Type: v1.IdentitySourceTypeOIDC}},
		"unknown type": {Spec: &v1.IdentitySourceSpec{Type: "saml"}},
		"no password":  {Spec: ldapSpec("ldap://127.0.0.1:1")},
	}

	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, ConnectionTest(context.Background(), source, nil))
		})
	}
}

func TestSafeMessage(t *testing.T) {
	secrets := &storage.IdentitySourceSecrets{LDAPBindPassword: "hunter2", OIDCClientSecret: "s3cr3t"}

	assert.Empty(t, SafeMessage(nil, secrets))
	assert.Equal(t, "bind [redacted] / [redacted]", SafeMessage(errors.New("bind hunter2 / s3cr3t"), secrets))
	assert.Equal(t, "plain", SafeMessage(errors.New("plain"), nil))

	long := SafeMessage(errors.New(strings.Repeat("é", maxMessageLength)), nil)
	assert.LessOrEqual(t, len(long), maxMessageLength+3)
	assert.True(t, strings.HasSuffix(long, "..."))
	assert.NotContains(t, long, "�")
}
