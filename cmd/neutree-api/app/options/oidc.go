package options

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/auth"
	authroutes "github.com/neutree-ai/neutree/internal/routes/auth"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
)

// OIDCClientSecretEnv supplies the client secret when --oidc-client-secret is
// not set, which keeps it out of the process arguments.
const OIDCClientSecretEnv = "NEUTREE_OIDC_CLIENT_SECRET" //nolint:gosec // an environment variable name, not a credential

// OIDCOptions configures the single OpenID Connect provider users can log in
// with. OIDC login is off unless Issuer is set.
type OIDCOptions struct {
	ID           string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	CAFile       string
	RedirectURL  string

	UsernameClaim    string
	DisplayNameClaim string
	EmailClaim       string

	AllowedRedirects []string
}

// NewOIDCOptions creates OIDC options with the standard OIDC claims.
func NewOIDCOptions() *OIDCOptions {
	return &OIDCOptions{
		Scopes:           []string{oidc.ScopeOpenID, "profile", "email"},
		UsernameClaim:    "preferred_username",
		DisplayNameClaim: "name",
		EmailClaim:       "email",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *OIDCOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.ID, "oidc-id", o.ID,
		"short name of the OIDC provider (lowercase letters, digits, '-'); it keys the users' links and new users' placeholder emails, "+
			"so changing it after users have logged in makes their next login create new users")
	fs.StringVar(&o.Issuer, "oidc-issuer", o.Issuer, "OIDC issuer URL, e.g. https://idp.example.org/realms/neutree; empty disables OIDC login")
	fs.StringVar(&o.ClientID, "oidc-client-id", o.ClientID, "OIDC client ID registered for neutree")
	fs.StringVar(&o.ClientSecret, "oidc-client-secret", o.ClientSecret,
		"OIDC client secret; prefer the "+OIDCClientSecretEnv+" environment variable")
	fs.StringSliceVar(&o.Scopes, "oidc-scopes", o.Scopes, "OIDC scopes to request; must include openid")
	fs.StringVar(&o.CAFile, "oidc-ca-file", o.CAFile, "PEM file of CAs trusted for the OIDC provider in addition to the system pool")
	fs.StringVar(&o.RedirectURL, "oidc-redirect-url", o.RedirectURL,
		"public URL of the neutree OIDC callback, e.g. https://neutree.example.org/api/v1/auth/oidc/callback; must be registered with the provider")
	fs.StringVar(&o.UsernameClaim, "oidc-claim-username", o.UsernameClaim, "claim holding the login name; empty skips it")
	fs.StringVar(&o.DisplayNameClaim, "oidc-claim-display-name", o.DisplayNameClaim, "claim holding the display name; empty skips it")
	fs.StringVar(&o.EmailClaim, "oidc-claim-email", o.EmailClaim, "claim holding the email; empty skips it")
	fs.StringSliceVar(&o.AllowedRedirects, "oidc-allowed-redirects", o.AllowedRedirects,
		"UI URLs an OIDC login may return to, e.g. https://neutree.example.org/; any URL under one of them is allowed, the first is the default")
}

// Enabled reports whether OIDC login is configured.
func (o *OIDCOptions) Enabled() bool {
	return o.Issuer != ""
}

// Validate validates OIDC options
func (o *OIDCOptions) Validate() error {
	cfg, err := o.OIDCConfig()
	if err != nil || cfg == nil {
		return err
	}

	if !auth.ValidOIDCProviderID(cfg.ID) {
		return fmt.Errorf("--oidc-id %q must be 1-32 lowercase letters, digits or inner '-'", cfg.ID)
	}

	if _, err := authroutes.ParseOIDCAllowedRedirects(cfg.AllowedRedirects); err != nil {
		return fmt.Errorf("--oidc-allowed-redirects: %w", err)
	}

	return cfg.Provider.Validate()
}

// OIDCConfig builds the provider config, or returns nil when OIDC is disabled.
func (o *OIDCOptions) OIDCConfig() (*config.OIDCConfig, error) {
	if !o.Enabled() {
		return nil, nil
	}

	cfg := &config.OIDCConfig{
		ID: o.ID,
		Provider: oidc.Config{
			Issuer:       o.Issuer,
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			RedirectURL:  o.RedirectURL,
			Scopes:       o.Scopes,
			Claims: oidc.ClaimMapping{
				Username:    o.UsernameClaim,
				DisplayName: o.DisplayNameClaim,
				Email:       o.EmailClaim,
			},
		},
		AllowedRedirects: o.AllowedRedirects,
	}

	if cfg.Provider.ClientSecret == "" {
		cfg.Provider.ClientSecret = os.Getenv(OIDCClientSecretEnv)
	}

	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read OIDC CA file: %w", err)
		}

		cfg.Provider.RootCAs = pem
	}

	return cfg, nil
}
