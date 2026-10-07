package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/utils/request"
)

const (
	// identitySourceKey is the GoTrue app_metadata key naming where a user's
	// identity is managed. Local users do not have it. GoTrue rewrites
	// app_metadata.provider on every session it issues, so the source needs a
	// key of its own.
	identitySourceKey = "identity_source"

	// ldapSource is the identity source of LDAP accounts, both in app_metadata
	// and in external_identities. There is a single LDAP directory for now.
	ldapSource = "ldap"
)

// externallyManagedFields lists, per identity source, the GoTrue account fields
// that source owns and users therefore cannot change through PUT /auth/user.
// A local password would let the user log in without the source (even after
// being disabled there), and a new email would detach the account from the
// placeholder its sessions are issued for. A source missing here is managed
// locally.
var externallyManagedFields = map[string][]string{
	ldapSource: {"email", "password", "phone"},
}

// identitySource returns the identity source recorded in a user's app_metadata,
// or "" for a local user.
func identitySource(appMetadata map[string]any) string {
	source, _ := appMetadata[identitySourceKey].(string)
	return source
}

// handleUpdateUser proxies PUT /auth/user, refusing changes to the fields the
// caller's identity source manages.
//
// The source comes from the caller's access token, validated like any other
// bearer token. A request whose token does not validate is proxied unchanged:
// GoTrue rejects it on its own.
func handleUpdateUser(deps *Dependencies) gin.HandlerFunc {
	proxyHandler := handleAuthProxy(deps)

	return func(c *gin.Context) {
		claims, err := middleware.ParseBearerClaims(deps.AuthConfig, c.GetHeader("Authorization"))
		if err != nil {
			proxyHandler(c)
			return
		}

		source := identitySource(claims.AppMetadata)

		protected := externallyManagedFields[source]
		if len(protected) == 0 {
			proxyHandler(c)
			return
		}

		bodyBytes, err := io.ReadAll(c.Request.Body)
		c.Request.Body.Close()

		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}

		var body map[string]json.RawMessage
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
			return
		}

		if field := protectedFieldIn(body, protected); field != "" {
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("%s is managed by the %s identity source and cannot be changed here", field, source),
			})

			return
		}

		request.RestoreBody(c, bodyBytes)
		proxyHandler(c)
	}
}

// protectedFieldIn returns the first protected field the body sets, or "".
// Keys match case-insensitively, as GoTrue decodes them with encoding/json.
func protectedFieldIn(body map[string]json.RawMessage, protected []string) string {
	for key, value := range body {
		if string(value) == "null" {
			continue
		}

		for _, field := range protected {
			if strings.EqualFold(key, field) {
				return field
			}
		}
	}

	return ""
}
