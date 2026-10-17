package v1

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// IdentitySourceType names the protocol an identity source speaks.
type IdentitySourceType string

const (
	IdentitySourceTypeLDAP IdentitySourceType = "ldap"
	IdentitySourceTypeOIDC IdentitySourceType = "oidc"
)

type IdentitySourcePhase string

const (
	IdentitySourcePhasePENDING   IdentitySourcePhase = "Pending"
	IdentitySourcePhaseCONNECTED IdentitySourcePhase = "Connected"
	IdentitySourcePhaseFAILED    IdentitySourcePhase = "Failed"
	IdentitySourcePhaseDELETED   IdentitySourcePhase = "Deleted"
)

// Defaults the database applies to an identity source spec when a field is
// left empty. They match the former --ldap-* / --oidc-* flag defaults.
const (
	DefaultIdentitySourceLDAPTimeoutSeconds = 10
	DefaultIdentitySourceLDAPIDAttribute    = "entryUUID"
	DefaultIdentitySourceLDAPUsernameAttr   = "uid"
	DefaultIdentitySourceLDAPEmailAttr      = "mail"
	DefaultIdentitySourceLDAPDisplayAttr    = "cn"
	DefaultIdentitySourceOIDCUsernameClaim  = "preferred_username"
	DefaultIdentitySourceOIDCDisplayClaim   = "name"
	DefaultIdentitySourceOIDCEmailClaim     = "email"

	// IdentitySourceLDAPUsernamePlaceholder must appear in an LDAP user filter.
	IdentitySourceLDAPUsernamePlaceholder = "{username}"
	// IdentitySourceOIDCScopeOpenID must be among the requested OIDC scopes.
	IdentitySourceOIDCScopeOpenID = "openid"
)

// DefaultIdentitySourceOIDCScopes are requested when spec.oidc.scopes is empty.
var DefaultIdentitySourceOIDCScopes = []string{"openid", "profile", "email"}

// IdentitySource is an external directory or provider users can log in with.
// It is a global resource: metadata.workspace is always empty.
type IdentitySource struct {
	ID         int                   `json:"id,omitempty"`
	APIVersion string                `json:"api_version,omitempty"`
	Kind       string                `json:"kind,omitempty"`
	Metadata   *Metadata             `json:"metadata,omitempty"`
	Spec       *IdentitySourceSpec   `json:"spec,omitempty"`
	Status     *IdentitySourceStatus `json:"status,omitempty"`
}

// IdentitySourceSpec holds exactly one of LDAP and OIDC, matching Type.
type IdentitySourceSpec struct {
	Type    IdentitySourceType      `json:"type"`
	Enabled bool                    `json:"enabled"`
	LDAP    *IdentitySourceLDAPSpec `json:"ldap,omitempty"`
	OIDC    *IdentitySourceOIDCSpec `json:"oidc,omitempty"`
}

// IdentitySourceLDAPSpec describes how to reach the directory and find users.
type IdentitySourceLDAPSpec struct {
	// URL is ldap://host:389 or ldaps://host:636.
	URL      string `json:"url"`
	StartTLS bool   `json:"start_tls,omitempty"`
	// CACert is a PEM bundle verifying the server certificate; empty uses the system pool.
	CACert             string `json:"ca_cert,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	// Timeout bounds dialing and each LDAP operation, in seconds.
	Timeout int `json:"timeout,omitempty"`

	BindDN string `json:"bind_dn"`
	// BindPassword is write-only: the database encrypts it into a separate
	// table and never returns it. Leaving it empty on an update keeps the
	// stored password.
	BindPassword string `json:"bind_password,omitempty" api:"-"`

	UserBaseDN string `json:"user_base_dn"`
	// UserFilter must contain {username}.
	UserFilter string                        `json:"user_filter"`
	Attributes *IdentitySourceLDAPAttributes `json:"attributes,omitempty"`
}

// IdentitySourceLDAPAttributes names the directory attributes read at login.
type IdentitySourceLDAPAttributes struct {
	ID          string `json:"id,omitempty"`
	Username    string `json:"username,omitempty"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	MemberOf    string `json:"member_of,omitempty"`
}

// IdentitySourceOIDCSpec describes an OpenID Connect provider neutree logs in
// with as a relying party.
type IdentitySourceOIDCSpec struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	// ClientSecret is write-only, like IdentitySourceLDAPSpec.BindPassword.
	ClientSecret string   `json:"client_secret,omitempty" api:"-"`
	Scopes       []string `json:"scopes,omitempty"`
	// CACert is a PEM bundle trusted in addition to the system pool.
	CACert string `json:"ca_cert,omitempty"`
	// RedirectURL is the public URL of neutree's OIDC callback, registered
	// with the provider. neutree-api does not know its own public URL, so it
	// has to be configured.
	RedirectURL      string                    `json:"redirect_url"`
	Claims           *IdentitySourceOIDCClaims `json:"claims,omitempty"`
	AllowedRedirects []string                  `json:"allowed_redirects"`
}

// IdentitySourceOIDCClaims names the ID token claims read at login.
type IdentitySourceOIDCClaims struct {
	Username    string `json:"username,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Email       string `json:"email,omitempty"`
}

type IdentitySourceStatus struct {
	Phase              IdentitySourcePhase           `json:"phase,omitempty"`
	LastTransitionTime string                        `json:"last_transition_time,omitempty"`
	ErrorMessage       string                        `json:"error_message,omitempty"`
	LastConnectionTest *IdentitySourceConnectionTest `json:"last_connection_test,omitempty"`
}

// IdentitySourceConnectionTest is the outcome of the last connection test.
type IdentitySourceConnectionTest struct {
	Time    string `json:"time,omitempty"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// LoginIdentitySource is what the unauthenticated login page learns about an
// enabled identity source, and nothing more.
type LoginIdentitySource struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name"`
	Type        IdentitySourceType `json:"type"`
}

// identitySourceNamePattern is a DNS label of at most 32 characters: the name
// becomes part of the users' link source (ldap:<name>) and of their
// placeholder email domain (<name>.<type>.neutree.local).
var identitySourceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// ValidateIdentitySourceName reports whether name can name an identity source.
func ValidateIdentitySourceName(name string) error {
	if !identitySourceNamePattern.MatchString(name) {
		return fmt.Errorf("identity source name %q must be 1-32 lowercase letters, digits or inner '-'", name)
	}

	return nil
}

// Validate checks the spec the way the database does, plus what SQL cannot:
// that URLs parse and CA certificates are PEM certificates. It does not
// require secrets, since an update may keep the stored ones.
func (s *IdentitySourceSpec) Validate() error {
	if s == nil {
		return errors.New("spec is required")
	}

	switch s.Type {
	case IdentitySourceTypeLDAP:
		if s.OIDC != nil {
			return errors.New("spec.oidc must be empty when spec.type is ldap")
		}

		return s.LDAP.validate()
	case IdentitySourceTypeOIDC:
		if s.LDAP != nil {
			return errors.New("spec.ldap must be empty when spec.type is oidc")
		}

		return s.OIDC.validate()
	default:
		return fmt.Errorf("spec.type must be %q or %q, got %q", IdentitySourceTypeLDAP, IdentitySourceTypeOIDC, s.Type)
	}
}

func (l *IdentitySourceLDAPSpec) validate() error {
	if l == nil {
		return errors.New("spec.ldap is required when spec.type is ldap")
	}

	required := []struct{ name, value string }{
		{"spec.ldap.url", l.URL},
		{"spec.ldap.bind_dn", l.BindDN},
		{"spec.ldap.user_base_dn", l.UserBaseDN},
		{"spec.ldap.user_filter", l.UserFilter},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}

	u, err := url.Parse(l.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Hostname() == "" {
		return fmt.Errorf("spec.ldap.url %q must be ldap://host[:port] or ldaps://host[:port]", l.URL)
	}

	if l.StartTLS && u.Scheme == "ldaps" {
		return errors.New("spec.ldap.start_tls cannot be used with an ldaps:// url")
	}

	if l.Timeout < 0 {
		return errors.New("spec.ldap.timeout must not be negative")
	}

	if !strings.Contains(l.UserFilter, IdentitySourceLDAPUsernamePlaceholder) {
		return fmt.Errorf("spec.ldap.user_filter must contain %s", IdentitySourceLDAPUsernamePlaceholder)
	}

	return validateCACert("spec.ldap.ca_cert", l.CACert)
}

func (o *IdentitySourceOIDCSpec) validate() error {
	if o == nil {
		return errors.New("spec.oidc is required when spec.type is oidc")
	}

	if strings.TrimSpace(o.ClientID) == "" {
		return errors.New("spec.oidc.client_id is required")
	}

	if err := validateHTTPURL("spec.oidc.issuer", o.Issuer); err != nil {
		return err
	}

	if err := validateHTTPURL("spec.oidc.redirect_url", o.RedirectURL); err != nil {
		return err
	}

	if len(o.Scopes) > 0 && !slices.Contains(o.Scopes, IdentitySourceOIDCScopeOpenID) {
		return fmt.Errorf("spec.oidc.scopes must include %q", IdentitySourceOIDCScopeOpenID)
	}

	if len(o.AllowedRedirects) == 0 {
		return errors.New("spec.oidc.allowed_redirects needs at least one URL")
	}

	for _, raw := range o.AllowedRedirects {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("spec.oidc.allowed_redirects entry %q must be an absolute http(s) URL without user, query or fragment", raw)
		}
	}

	return validateCACert("spec.oidc.ca_cert", o.CACert)
}

func validateHTTPURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s %q must be an absolute http(s) URL", field, raw)
	}

	return nil
}

func validateCACert(field, bundle string) error {
	if strings.TrimSpace(bundle) == "" {
		return nil
	}

	rest := []byte(bundle)
	found := false

	for {
		var block *pem.Block

		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}

		if block.Type != "CERTIFICATE" {
			continue
		}

		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("%s holds an invalid certificate: %w", field, err)
		}

		found = true
	}

	if !found {
		return fmt.Errorf("%s holds no PEM certificate", field)
	}

	return nil
}

func (obj *IdentitySource) GetName() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Name
}

func (obj *IdentitySource) GetWorkspace() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.Workspace
}

func (obj *IdentitySource) GetLabels() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Labels
}

func (obj *IdentitySource) SetLabels(labels map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Labels = labels
}

func (obj *IdentitySource) GetAnnotations() map[string]string {
	if obj.Metadata == nil {
		return nil
	}

	return obj.Metadata.Annotations
}

func (obj *IdentitySource) SetAnnotations(annotations map[string]string) {
	if obj.Metadata == nil {
		obj.Metadata = &Metadata{}
	}

	obj.Metadata.Annotations = annotations
}

func (obj *IdentitySource) GetCreationTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.CreationTimestamp
}

func (obj *IdentitySource) GetUpdateTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.UpdateTimestamp
}

func (obj *IdentitySource) GetDeletionTimestamp() string {
	if obj.Metadata == nil {
		return ""
	}

	return obj.Metadata.DeletionTimestamp
}

func (obj *IdentitySource) GetSpec() interface{} {
	return obj.Spec
}

func (obj *IdentitySource) GetStatus() interface{} {
	return obj.Status
}

func (obj *IdentitySource) GetKind() string {
	return obj.Kind
}

func (obj *IdentitySource) SetKind(kind string) {
	obj.Kind = kind
}

func (obj *IdentitySource) GetID() string {
	return strconv.Itoa(obj.ID)
}

func (obj *IdentitySource) SetID(id string) {
	obj.ID, _ = strconv.Atoi(id)
}

func (obj *IdentitySource) GetMetadata() interface{} {
	return obj.Metadata
}

// IdentitySourceList is a list of IdentitySource resources
type IdentitySourceList struct {
	Kind  string           `json:"kind"`
	Items []IdentitySource `json:"items"`
}

func (in *IdentitySourceList) GetKind() string {
	return in.Kind
}

func (in *IdentitySourceList) SetKind(kind string) {
	in.Kind = kind
}

func (in *IdentitySourceList) GetItems() []scheme.Object {
	var objs []scheme.Object
	for i := range in.Items {
		objs = append(objs, &in.Items[i])
	}

	return objs
}

func (in *IdentitySourceList) SetItems(objs []scheme.Object) {
	items := make([]IdentitySource, len(objs))
	for i, obj := range objs {
		items[i] = *obj.(*IdentitySource) //nolint:errcheck
	}

	in.Items = items
}
