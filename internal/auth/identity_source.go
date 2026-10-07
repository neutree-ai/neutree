package auth

import "slices"

const (
	// IdentitySourceKey is the GoTrue app_metadata key naming where a user's
	// identity is managed. Local users do not have it. GoTrue rewrites
	// app_metadata.provider on every session it issues, so the source needs a
	// key of its own.
	IdentitySourceKey = "identity_source"

	// LDAPSource is the identity source of LDAP accounts, both in app_metadata
	// and in external_identities. There is a single LDAP directory for now.
	LDAPSource = "ldap"
)

// externallyManagedFields lists, per identity source, the GoTrue account fields
// that source owns. A local password would let the user log in without the
// source (even after being disabled there), and a new email would detach the
// account from the placeholder its sessions are issued for. A source missing
// here is managed locally.
var externallyManagedFields = map[string][]string{
	LDAPSource: {"email", "password", "phone"},
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
