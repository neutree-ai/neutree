package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func RegisterStaticNodeClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/static_node_clusters")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.StaticNodeCluster](deps, storage.STATIC_NODE_CLUSTER_TABLE)
	proxyGroup.GET("", handler)
}
