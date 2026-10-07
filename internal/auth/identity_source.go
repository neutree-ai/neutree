package auth

import (
	"regexp"
	"slices"
)

const (
	// IdentitySourceKey is the GoTrue app_metadata key naming where a user's
	// identity is managed. Local users do not have it. GoTrue rewrites
	// app_metadata.provider on every session it issues, so the source needs a
	// key of its own.
	IdentitySourceKey = "identity_source"

	// LDAPSource is the identity source of LDAP accounts, both in app_metadata
	// and in external_identities. There is a single LDAP directory for now.
	LDAPSource = "ldap"

	// OIDCSource is the identity source of OpenID Connect accounts in
	// app_metadata. Their external_identities source is OIDCLinkSource of the
	// provider, so each configured provider keeps its own links.
	OIDCSource = "oidc"
)

// OIDCLinkSource returns the external_identities source of the OpenID Connect
// provider with the given ID.
func OIDCLinkSource(providerID string) string {
	return OIDCSource + ":" + providerID
}

// externallyManagedFields lists, per identity source, the GoTrue account fields
// that source owns. A local password would let the user log in without the
// source (even after being disabled there), and the email is a placeholder
// neutree assigns: one the user picks would let mail sent to it, such as
// recovery links, act for the account. A source missing here is managed
// locally.
var externallyManagedFields = map[string][]string{
	LDAPSource: {"email", "password", "phone"},
	OIDCSource: {"email", "password", "phone"},
}

// IdentitySource returns the identity source recorded in a user's app_metadata,
// or "" for a local user.
func IdentitySource(appMetadata map[string]any) string {
	source, _ := appMetadata[IdentitySourceKey].(string)
	return source
}

// ExternallyManagedFields returns the GoTrue account fields that source owns,
// which neutree must not change for its users.
func ExternallyManagedFields(source string) []string {
	return externallyManagedFields[source]
}

// ManagesField reports whether source owns the GoTrue account field.
func ManagesField(source, field string) bool {
	return slices.Contains(externallyManagedFields[source], field)
}

// oidcProviderIDPattern is a DNS label: the provider ID becomes part of the
// placeholder email domain of its users.
var oidcProviderIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// ValidOIDCProviderID reports whether id can name an OpenID Connect provider:
// lowercase letters, digits and inner '-', at most 32 characters.
func ValidOIDCProviderID(id string) bool {
	return oidcProviderIDPattern.MatchString(id)
}
