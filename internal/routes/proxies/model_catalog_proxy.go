package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// RegisterModelCatalogRoutes registers model catalog routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterModelCatalogRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/model_catalogs")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ModelCatalog](deps, "model_catalogs")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}
