package v1

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCACertPEM(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func validLDAPSpec() *IdentitySourceSpec {
	return &IdentitySourceSpec{
		Type:    IdentitySourceTypeLDAP,
		Enabled: true,
		LDAP: &IdentitySourceLDAPSpec{
			URL:        "ldaps://ldap.example.org:636",
			BindDN:     "cn=svc,dc=example,dc=org",
			UserBaseDN: "ou=people,dc=example,dc=org",
			UserFilter: "(uid={username})",
		},
	}
}

func validOIDCSpec() *IdentitySourceSpec {
	return &IdentitySourceSpec{
		Type:    IdentitySourceTypeOIDC,
		Enabled: true,
		OIDC: &IdentitySourceOIDCSpec{
			Issuer:           "https://idp.example.org/realms/neutree",
			ClientID:         "neutree",
			RedirectURL:      "https://neutree.example.org/api/v1/auth/oidc/callback",
			AllowedRedirects: []string{"https://neutree.example.org/"},
		},
	}
}

func TestValidateIdentitySourceName(t *testing.T) {
	valid := []string{"a", "corp", "corp-ldap", "ad1", strings.Repeat("a", 32)}
	for _, name := range valid {
		assert.NoError(t, ValidateIdentitySourceName(name), name)
	}

	invalid := []string{"", "-corp", "corp-", "Corp", "corp.ldap", "corp_ldap", strings.Repeat("a", 33), "corp ldap"}
	for _, name := range invalid {
		assert.Error(t, ValidateIdentitySourceName(name), name)
	}
}

func TestIdentitySourceSpecValidate(t *testing.T) {
	ca := testCACertPEM(t)

	tests := []struct {
		name    string
		mutate  func(*IdentitySourceSpec)
		base    func() *IdentitySourceSpec
		wantErr string
	}{
		{"valid ldap", func(*IdentitySourceSpec) {}, validLDAPSpec, ""},
		{"valid ldap with CA and StartTLS", func(s *IdentitySourceSpec) {
			s.LDAP.URL = "ldap://ldap.example.org"
			s.LDAP.StartTLS = true
			s.LDAP.CACert = ca
		}, validLDAPSpec, ""},
		{"valid oidc", func(*IdentitySourceSpec) {}, validOIDCSpec, ""},
		{"valid oidc with scopes and CA", func(s *IdentitySourceSpec) {
			s.OIDC.Scopes = []string{"openid", "groups"}
			s.OIDC.CACert = ca
		}, validOIDCSpec, ""},
		{"unknown type", func(s *IdentitySourceSpec) { s.Type = "saml" }, validLDAPSpec, "spec.type"},
		{"ldap missing sub-object", func(s *IdentitySourceSpec) { s.LDAP = nil }, validLDAPSpec, "spec.ldap is required"},
		{"ldap with oidc sub-object", func(s *IdentitySourceSpec) { s.OIDC = &IdentitySourceOIDCSpec{} }, validLDAPSpec, "spec.oidc must be empty"},
		{"oidc with ldap sub-object", func(s *IdentitySourceSpec) { s.LDAP = &IdentitySourceLDAPSpec{} }, validOIDCSpec, "spec.ldap must be empty"},
		{"ldap missing bind dn", func(s *IdentitySourceSpec) { s.LDAP.BindDN = " " }, validLDAPSpec, "bind_dn is required"},
		{"ldap bad scheme", func(s *IdentitySourceSpec) { s.LDAP.URL = "http://ldap.example.org" }, validLDAPSpec, "ldaps://"},
		{"ldap no host", func(s *IdentitySourceSpec) { s.LDAP.URL = "ldap://" }, validLDAPSpec, "ldaps://"},
		{"ldaps with starttls", func(s *IdentitySourceSpec) { s.LDAP.StartTLS = true }, validLDAPSpec, "start_tls"},
		{"filter without placeholder", func(s *IdentitySourceSpec) { s.LDAP.UserFilter = "(uid=*)" }, validLDAPSpec, "{username}"},
		{"negative timeout", func(s *IdentitySourceSpec) { s.LDAP.Timeout = -1 }, validLDAPSpec, "timeout"},
		{"ldap CA not PEM", func(s *IdentitySourceSpec) { s.LDAP.CACert = "not a cert" }, validLDAPSpec, "no PEM certificate"},
		{"ldap CA bad DER", func(s *IdentitySourceSpec) {
			s.LDAP.CACert = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}))
		}, validLDAPSpec, "invalid certificate"},
		{"oidc missing sub-object", func(s *IdentitySourceSpec) { s.OIDC = nil }, validOIDCSpec, "spec.oidc is required"},
		{"oidc missing client id", func(s *IdentitySourceSpec) { s.OIDC.ClientID = "" }, validOIDCSpec, "client_id"},
		{"oidc issuer not http", func(s *IdentitySourceSpec) { s.OIDC.Issuer = "ftp://idp" }, validOIDCSpec, "issuer"},
		{"oidc redirect url missing", func(s *IdentitySourceSpec) { s.OIDC.RedirectURL = "" }, validOIDCSpec, "redirect_url"},
		{"oidc scopes without openid", func(s *IdentitySourceSpec) { s.OIDC.Scopes = []string{"profile"} }, validOIDCSpec, "openid"},
		{"oidc no allowed redirects", func(s *IdentitySourceSpec) { s.OIDC.AllowedRedirects = nil }, validOIDCSpec, "allowed_redirects"},
		{"oidc allowed redirect with query", func(s *IdentitySourceSpec) {
			s.OIDC.AllowedRedirects = []string{"https://neutree.example.org/?a=b"}
		}, validOIDCSpec, "allowed_redirects"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := tt.base()
			tt.mutate(spec)

			err := spec.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	var nilSpec *IdentitySourceSpec
	assert.Error(t, nilSpec.Validate())
}

// The secrets are masked by the resource proxy because of their api:"-" tag;
// this pins the JSON names the tag paths are derived from.
func TestIdentitySourceSecretJSONNames(t *testing.T) {
	spec := validLDAPSpec()
	spec.LDAP.BindPassword = "pw"

	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"bind_password":"pw"`)

	spec = validOIDCSpec()
	spec.OIDC.ClientSecret = "s"

	raw, err = json.Marshal(spec)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"client_secret":"s"`)
	assert.NotContains(t, string(raw), `"ldap"`)
}

func TestIdentitySourceSpecValidate_Sync(t *testing.T) {
	spec := validLDAPSpec()
	spec.Sync = &IdentitySourceSyncSpec{Enabled: true, Interval: 600}
	spec.LDAP.Sync = &IdentitySourceLDAPSyncSpec{OrgUnitBaseDN: "ou=org,dc=example,dc=org", PageSize: 100}
	assert.NoError(t, spec.Validate())
	assert.True(t, spec.SyncEnabled())
	assert.Equal(t, 600*time.Second, spec.Sync.SyncInterval())

	spec.Sync.Interval = 30
	assert.ErrorContains(t, spec.Validate(), "spec.sync.interval")

	spec.Sync.Interval = 0
	assert.NoError(t, spec.Validate())
	assert.Equal(t, DefaultIdentitySourceSyncIntervalSeconds*time.Second, spec.Sync.SyncInterval())

	spec.LDAP.Sync.UserListFilter = "(uid={username})"
	assert.ErrorContains(t, spec.Validate(), "user_list_filter")

	spec.LDAP.Sync.UserListFilter = ""
	spec.LDAP.Sync.PageSize = -1
	assert.ErrorContains(t, spec.Validate(), "page_size")

	oidc := validOIDCSpec()
	oidc.Sync = &IdentitySourceSyncSpec{Enabled: true}
	assert.NoError(t, oidc.Validate())
	assert.False(t, oidc.SyncEnabled(), "only LDAP sources sync")
}
