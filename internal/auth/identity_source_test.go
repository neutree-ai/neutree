package auth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestManagesField(t *testing.T) {
	for _, source := range []string{LDAPSource, OIDCSource} {
		for _, field := range []string{"email", "password", "phone"} {
			assert.True(t, ManagesField(source, field), "%s manages %s", source, field)
		}

		assert.False(t, ManagesField(source, "data"))
	}

	assert.False(t, ManagesField("", "password"), "local users manage their own password")
}

func TestLinkSource(t *testing.T) {
	assert.Equal(t, "ldap:corp-ldap", LinkSource(LDAPSource, "corp-ldap"))
	assert.Equal(t, "oidc:keycloak", LinkSource(OIDCSource, "keycloak"))
}
