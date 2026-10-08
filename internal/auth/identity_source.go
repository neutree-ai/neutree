package auth

import (
	"slices"
)

const (
	// IdentitySourceKey is the GoTrue app_metadata key naming where a user's
	// identity is managed. Local users do not have it. GoTrue rewrites
	// app_metadata.provider on every session it issues, so the source needs a
	// key of its own.
	IdentitySourceKey = "identity_source"

	// LDAPSource and OIDCSource are the identity_source app_metadata values of
	// users from an LDAP or an OpenID Connect IdentitySource: the source's type,
	// which selects the fields neutree must not change for the user. The
	// account's link in external_identities is keyed by LinkSource instead, so
	// each IdentitySource keeps its own links.
	LDAPSource = "ldap"
	OIDCSource = "oidc"
)

// LinkSource returns the external_identities source of the accounts of the
// IdentitySource of the given type and name, e.g. ldap:corp-ldap.
func LinkSource(sourceType, name string) string {
	return sourceType + ":" + name
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
