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
	// which selects the fields neutree must not change for the user.
	LDAPSource = "ldap"
	OIDCSource = "oidc"

	// IdentitySourceLabel is the read-only user profile label naming the
	// identity source of an external user. The database sets it from
	// app_metadata on every profile write (migration 108), so no write can
	// add, change or remove it; local users never have it.
	IdentitySourceLabel = "neutree.ai/identity-source"
)

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
