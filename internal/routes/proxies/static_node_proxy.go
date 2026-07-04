package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func RegisterStaticNodeRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/static_nodes")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.StaticNode](deps, storage.STATIC_NODE_TABLE)
	proxyGroup.GET("", handler)
}
