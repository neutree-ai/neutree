package proxies

import (
	"github.com/gin-gonic/gin"

	v1beta1 "github.com/neutree-ai/neutree/api/v1beta1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterExternalEndpointRoutes registers external endpoint routes
// The auth.credential field is masked in API responses (api:"-" tag)
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterExternalEndpointRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/external_endpoints")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1beta1.ExternalEndpoint](deps, storage.EXTERNAL_ENDPOINT_TABLE)

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}
