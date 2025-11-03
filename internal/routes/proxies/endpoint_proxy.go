package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RegisterEndpointRoutes registers endpoint routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterEndpointRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/endpoints")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.Endpoint](deps, "endpoints")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}
