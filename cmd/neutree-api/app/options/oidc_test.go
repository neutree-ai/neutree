package options

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseOIDCFlags(t *testing.T, args ...string) *OIDCOptions {
	t.Helper()

	o := NewOIDCOptions()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.AddFlags(fs)
	require.NoError(t, fs.Parse(args))

	return o
}

var validOIDCArgs = []string{
	"--oidc-id=keycloak",
	"--oidc-issuer=https://idp.example.org/realms/neutree",
	"--oidc-client-id=neutree",
	"--oidc-redirect-url=https://neutree.example.org/api/v1/auth/oidc/callback",
	"--oidc-allowed-redirects=https://neutree.example.org/",
}

func TestOIDCOptions_DisabledByDefault(t *testing.T) {
	o := parseOIDCFlags(t)

	cfg, err := o.OIDCConfig()

	require.NoError(t, err)
	assert.Nil(t, cfg)
	assert.NoError(t, o.Validate())
}

func TestOIDCOptions_Config(t *testing.T) {
	t.Setenv(OIDCClientSecretEnv, "from-env")

	o := parseOIDCFlags(t, append(validOIDCArgs, "--oidc-claim-email=", "--oidc-allowed-redirects=https://other.example.org/ui")...)

	cfg, err := o.OIDCConfig()

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "keycloak", cfg.ID)
	assert.Equal(t, "from-env", cfg.Provider.ClientSecret)
	assert.Equal(t, []string{"openid", "profile", "email"}, cfg.Provider.Scopes)
	assert.Equal(t, "preferred_username", cfg.Provider.Claims.Username)
	assert.Equal(t, "name", cfg.Provider.Claims.DisplayName)
	assert.Empty(t, cfg.Provider.Claims.Email)
	assert.Equal(t, []string{"https://neutree.example.org/", "https://other.example.org/ui"}, cfg.AllowedRedirects)
	assert.NoError(t, o.Validate())
}

func TestOIDCOptions_FlagSecretWinsOverEnv(t *testing.T) {
	t.Setenv(OIDCClientSecretEnv, "from-env")

	cfg, err := parseOIDCFlags(t, append(validOIDCArgs, "--oidc-client-secret=from-flag")...).OIDCConfig()

	require.NoError(t, err)
	assert.Equal(t, "from-flag", cfg.Provider.ClientSecret)
}

func TestOIDCOptions_Invalid(t *testing.T) {
	cases := map[string][]string{
		"missing id":                {"--oidc-id="},
		"id not a DNS label":        {"--oidc-id=Key_Cloak"},
		"missing client id":         {"--oidc-client-id="},
		"missing redirect url":      {"--oidc-redirect-url="},
		"scopes without openid":     {"--oidc-scopes=profile,email"},
		"relative allowed redirect": {"--oidc-allowed-redirects=/ui/"},
		"missing CA file":           {"--oidc-ca-file=/nonexistent/ca.pem"},
	}

	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Error(t, parseOIDCFlags(t, append(validOIDCArgs, extra...)...).Validate())
		})
	}

	t.Run("missing allowed redirects", func(t *testing.T) {
		assert.Error(t, parseOIDCFlags(t, validOIDCArgs[:4]...).Validate())
	})
}
