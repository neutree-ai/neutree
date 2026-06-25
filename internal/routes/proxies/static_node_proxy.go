package proxies

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// RegisterStaticNodeClusterRoutes registers static node cluster routes.
//
// Allowed methods: GET, POST, PATCH
func RegisterStaticNodeClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/static_node_clusters")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.StaticNodeCluster](deps, storage.STATIC_NODE_CLUSTER_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}

// RegisterStaticNodeRoutes registers static node routes.
//
// Allowed methods: GET, POST, PATCH
func RegisterStaticNodeRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/static_nodes")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.StaticNode](deps, storage.STATIC_NODE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)
}
