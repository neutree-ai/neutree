package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RegisterModelRegistryRoutes registers model registry routes with field filtering based on struct tags
// Fields marked with api:"-" tag in v1.ModelRegistry will be automatically excluded from responses
//
// Masked fields:
//   - spec.credentials: HuggingFace tokens and other credentials
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterModelRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/model_registries")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ModelRegistry](deps, "model_registries")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}
