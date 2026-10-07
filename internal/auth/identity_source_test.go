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

func TestOIDCProviderID(t *testing.T) {
	assert.Equal(t, "oidc:keycloak", OIDCLinkSource("keycloak"))

	for _, id := range []string{"keycloak", "a", "corp-sso", "idp2"} {
		assert.True(t, ValidOIDCProviderID(id), id)
	}

	for _, id := range []string{"", "Keycloak", "-sso", "sso-", "corp_sso", "corp.sso", "a23456789012345678901234567890123"} {
		assert.False(t, ValidOIDCProviderID(id), id)
	}
}
