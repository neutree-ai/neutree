package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RegisterImageRegistryRoutes registers image registry routes with field filtering based on struct tags
// Fields marked with api:"-" tag in v1.ImageRegistry will be automatically excluded from responses
//
// Masked fields:
//   - spec.authconfig: All authentication credentials
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterImageRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/image_registries")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ImageRegistry](deps, "image_registries")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}
