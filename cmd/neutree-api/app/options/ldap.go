package options

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/pkg/identity/ldap"
)

// LDAPBindPasswordEnv supplies the service account password when
// --ldap-bind-password is not set, which keeps it out of the process arguments.
const LDAPBindPasswordEnv = "NEUTREE_LDAP_BIND_PASSWORD" //nolint:gosec // an environment variable name, not a credential

// LDAPOptions configures the single LDAP directory users can log in with.
// LDAP login is off unless URL is set.
type LDAPOptions struct {
	URL                string
	StartTLS           bool
	CAFile             string
	InsecureSkipVerify bool
	Timeout            time.Duration

	BindDN       string
	BindPassword string

	UserBaseDN string
	UserFilter string

	IDAttribute          string
	UsernameAttribute    string
	EmailAttribute       string
	DisplayNameAttribute string
}

// NewLDAPOptions creates LDAP options with attribute defaults for OpenLDAP.
func NewLDAPOptions() *LDAPOptions {
	return &LDAPOptions{
		Timeout:              ldap.DefaultTimeout,
		IDAttribute:          "entryUUID",
		UsernameAttribute:    "uid",
		EmailAttribute:       "mail",
		DisplayNameAttribute: "cn",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *LDAPOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.URL, "ldap-url", o.URL, "LDAP server URL, ldap://host:389 or ldaps://host:636; empty disables LDAP login")
	fs.BoolVar(&o.StartTLS, "ldap-start-tls", o.StartTLS, "upgrade an ldap:// connection with StartTLS")
	fs.StringVar(&o.CAFile, "ldap-ca-file", o.CAFile, "PEM file of CAs that verify the LDAP server certificate; empty uses the system pool")
	fs.BoolVar(&o.InsecureSkipVerify, "ldap-insecure-skip-verify", o.InsecureSkipVerify, "skip LDAP server certificate verification (testing only)")
	fs.DurationVar(&o.Timeout, "ldap-timeout", o.Timeout, "timeout for connecting to the LDAP server and for each LDAP operation")
	fs.StringVar(&o.BindDN, "ldap-bind-dn", o.BindDN, "DN of the service account that searches for users")
	fs.StringVar(&o.BindPassword, "ldap-bind-password", o.BindPassword,
		"password of the service account; prefer the "+LDAPBindPasswordEnv+" environment variable")
	fs.StringVar(&o.UserBaseDN, "ldap-user-base-dn", o.UserBaseDN, "base DN of the user search")
	fs.StringVar(&o.UserFilter, "ldap-user-filter", o.UserFilter,
		"user search filter containing "+ldap.UsernamePlaceholder+", e.g. (&(objectClass=inetOrgPerson)(uid="+ldap.UsernamePlaceholder+"))")
	fs.StringVar(&o.IDAttribute, "ldap-attr-id", o.IDAttribute, "attribute holding the stable user ID: entryUUID (OpenLDAP) or objectGUID (AD)")
	fs.StringVar(&o.UsernameAttribute, "ldap-attr-username", o.UsernameAttribute, "attribute holding the login name, e.g. uid or sAMAccountName")
	fs.StringVar(&o.EmailAttribute, "ldap-attr-email", o.EmailAttribute, "attribute holding the email; empty skips it")
	fs.StringVar(&o.DisplayNameAttribute, "ldap-attr-display-name", o.DisplayNameAttribute, "attribute holding the display name; empty skips it")
}

// Enabled reports whether LDAP login is configured.
func (o *LDAPOptions) Enabled() bool {
	return o.URL != ""
}

// Validate validates LDAP options
func (o *LDAPOptions) Validate() error {
	cfg, err := o.LDAPConfig()
	if err != nil {
		return err
	}

	if cfg == nil {
		return nil
	}

	return cfg.Validate()
}

// LDAPConfig builds the directory config, or returns nil when LDAP is disabled.
func (o *LDAPOptions) LDAPConfig() (*ldap.Config, error) {
	if !o.Enabled() {
		return nil, nil
	}

	cfg := &ldap.Config{
		URL:                o.URL,
		StartTLS:           o.StartTLS,
		InsecureSkipVerify: o.InsecureSkipVerify,
		Timeout:            o.Timeout,
		BindDN:             o.BindDN,
		BindPassword:       o.BindPassword,
		UserBaseDN:         o.UserBaseDN,
		UserFilter:         o.UserFilter,
		Attributes: ldap.AttributeMapping{
			ID:          o.IDAttribute,
			Username:    o.UsernameAttribute,
			Email:       o.EmailAttribute,
			DisplayName: o.DisplayNameAttribute,
		},
	}

	if cfg.BindPassword == "" {
		cfg.BindPassword = os.Getenv(LDAPBindPasswordEnv)
	}

	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read LDAP CA file: %w", err)
		}

		cfg.RootCAs = pem
	}

	return cfg, nil
}
