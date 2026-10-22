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
		if err := checkIdentitySourcePayload(c.Request); err != nil {
			c.JSON(validationErrStatus(err), err)
			c.Abort()

			return
		}

		c.Next()
	}
}

func checkIdentitySourcePayload(req *http.Request) *validationError {
	body, err := readAndRestoreBody(req)
	if err != nil {
		return invalidIdentitySourceError("failed to read request body")
	}

	var payload struct {
		Metadata *v1.Metadata    `json:"metadata"`
		Spec     json.RawMessage `json:"spec"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return invalidIdentitySourceError("request body must be a single identity source object")
	}

	hasSpec := len(payload.Spec) > 0 && string(payload.Spec) != "null"

	if req.Method == http.MethodPost {
		if payload.Metadata == nil {
			return invalidIdentitySourceError("metadata is required")
		}

		if err := v1.ValidateIdentitySourceName(payload.Metadata.Name); err != nil {
			return invalidIdentitySourceError(err.Error())
		}

		if !hasSpec {
			return invalidIdentitySourceError("spec is required")
		}
	}

	if !hasSpec {
		return nil
	}

	var spec v1.IdentitySourceSpec
	if err := json.Unmarshal(payload.Spec, &spec); err != nil {
		return invalidIdentitySourceError("spec is malformed: " + err.Error())
	}

	if err := spec.Validate(); err != nil {
		return invalidIdentitySourceError(err.Error())
	}

	return nil
}

func invalidIdentitySourceError(hint string) *validationError {
	return &validationError{
		Code:    identitySourceInvalidCode,
		Message: "invalid identity source",
		Hint:    hint,
	}
}
