package proxies

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const identitySourceInvalidCode = "10261"

// RegisterIdentitySourceRoutes registers identity source routes. The secrets
// (spec.ldap.bind_password, spec.oidc.client_secret) are masked in responses
// (api:"-"); the database never returns them anyway, and keeps the stored
// value when a write leaves them empty.
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterIdentitySourceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/identity_sources")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.IdentitySource](deps, storage.IDENTITY_SOURCE_TABLE)
	validation := validateIdentitySource()

	proxyGroup.GET("", handler)
	proxyGroup.POST("", validation, handler)
	proxyGroup.PATCH("", validation, handler)
}

// validateIdentitySource rejects a spec the login code could not use before it
// reaches the database, whose trigger enforces the same rules except parsing
// URLs and CA certificates. A write without spec (a status update, a soft
// delete) passes through.
func validateIdentitySource() gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := readAndRestoreBody(c.Request)
		if err != nil {
			rejectIdentitySource(c, "failed to read request body")
			return
		}

		var payload struct {
			Metadata *v1.Metadata    `json:"metadata"`
			Spec     json.RawMessage `json:"spec"`
		}

		if err := json.Unmarshal(body, &payload); err != nil {
			rejectIdentitySource(c, "request body must be a single identity source object")
			return
		}

		if c.Request.Method == http.MethodPost {
			if payload.Metadata == nil {
				rejectIdentitySource(c, "metadata is required")
				return
			}

			if err := v1.ValidateIdentitySourceName(payload.Metadata.Name); err != nil {
				rejectIdentitySource(c, err.Error())
				return
			}
		}

		if len(payload.Spec) == 0 || string(payload.Spec) == "null" {
			if c.Request.Method == http.MethodPost {
				rejectIdentitySource(c, "spec is required")
				return
			}

			c.Next()

			return
		}

		var spec v1.IdentitySourceSpec
		if err := json.Unmarshal(payload.Spec, &spec); err != nil {
			rejectIdentitySource(c, "spec is malformed: "+err.Error())
			return
		}

		if err := spec.Validate(); err != nil {
			rejectIdentitySource(c, err.Error())
			return
		}

		c.Next()
	}
}

func rejectIdentitySource(c *gin.Context, hint string) {
	c.JSON(http.StatusBadRequest, &validationError{
		Code:    identitySourceInvalidCode,
		Message: "invalid identity source",
		Hint:    hint,
	})
	c.Abort()
}
