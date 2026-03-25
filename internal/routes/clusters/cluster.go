package clusters

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Storage      storage.Storage
	ImageService registry.ImageService
}

func RegisterClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	clusterGroup := group.Group("/clusters")
	clusterGroup.Use(middlewares...)

	clusterGroup.GET("/available_versions", getAvailableClusterVersions(deps))
}
