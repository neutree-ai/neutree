package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func RegisterAPIKeyRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/api_keys")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ApiKey](deps, "api_keys")

	proxyGroup.GET("", handler)
	proxyGroup.PATCH("", handler)
}
