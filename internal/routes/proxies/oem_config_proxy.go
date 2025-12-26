package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RegisterOEMConfigRoutes registers OEM config routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterOEMConfigRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/oem_configs")

	handler := CreateStructProxyHandler[v1.OEMConfig](deps, "oem_configs")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", append(middlewares, handler)...)
	proxyGroup.PATCH("", append(middlewares, handler)...)
}
