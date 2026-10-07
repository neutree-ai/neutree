package options

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parseLDAPFlags(t *testing.T, args ...string) *LDAPOptions {
	t.Helper()

	o := NewLDAPOptions()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	o.AddFlags(fs)
	require.NoError(t, fs.Parse(args))

	return o
}

var validLDAPArgs = []string{
	"--ldap-url=ldap://ldap.example.org:389",
	"--ldap-bind-dn=cn=svc,dc=example,dc=org",
	"--ldap-user-base-dn=ou=people,dc=example,dc=org",
	"--ldap-user-filter=(uid={username})",
}

func TestLDAPOptions_DisabledByDefault(t *testing.T) {
	o := parseLDAPFlags(t)

	cfg, err := o.LDAPConfig()

	require.NoError(t, err)
	assert.Nil(t, cfg)
	assert.NoError(t, o.Validate())
}

func TestLDAPOptions_Config(t *testing.T) {
	t.Setenv(LDAPBindPasswordEnv, "from-env")

	o := parseLDAPFlags(t, append(validLDAPArgs, "--ldap-start-tls", "--ldap-attr-id=objectGUID", "--ldap-attr-email=")...)

	cfg, err := o.LDAPConfig()

	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.True(t, cfg.StartTLS)
	assert.Equal(t, "from-env", cfg.BindPassword)
	assert.Equal(t, "objectGUID", cfg.Attributes.ID)
	assert.Equal(t, "uid", cfg.Attributes.Username)
	assert.Empty(t, cfg.Attributes.Email)
	assert.NoError(t, o.Validate())
}

func TestLDAPOptions_FlagPasswordWinsOverEnv(t *testing.T) {
	t.Setenv(LDAPBindPasswordEnv, "from-env")

	cfg, err := parseLDAPFlags(t, append(validLDAPArgs, "--ldap-bind-password=from-flag")...).LDAPConfig()

	require.NoError(t, err)
	assert.Equal(t, "from-flag", cfg.BindPassword)
}

func TestLDAPOptions_CAFile(t *testing.T) {
	t.Setenv(LDAPBindPasswordEnv, "pw")

	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, []byte("not a certificate"), 0o600))

	o := parseLDAPFlags(t, append(validLDAPArgs, "--ldap-ca-file="+path)...)

	cfg, err := o.LDAPConfig()
	require.NoError(t, err)
	assert.Equal(t, []byte("not a certificate"), cfg.RootCAs)
	assert.Error(t, o.Validate(), "a CA file without a PEM certificate is rejected")

	_, err = parseLDAPFlags(t, append(validLDAPArgs, "--ldap-ca-file=/nonexistent/ca.pem")...).LDAPConfig()
	assert.Error(t, err)
}

func TestLDAPOptions_MissingPasswordRejected(t *testing.T) {
	t.Setenv(LDAPBindPasswordEnv, "")

	assert.Error(t, parseLDAPFlags(t, validLDAPArgs...).Validate())
}
