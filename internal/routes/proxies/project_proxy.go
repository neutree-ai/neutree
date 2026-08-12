package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func RegisterProjectRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/projects")
	proxyGroup.Use(middlewares...)
	handler := CreateStructProxyHandler[v1.Project](deps, storage.PROJECT_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
	proxyGroup.DELETE("", handler)
}
