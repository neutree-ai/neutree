package clusters

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ReleaseInfoProvider interface {
	Current() (*v1.ReleaseInfo, error)
}

type Dependencies struct {
	Storage             storage.Storage
	ImageService        registry.ImageService
	ReleaseInfoProvider ReleaseInfoProvider
}

func RegisterClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	clusterGroup := group.Group("/clusters")
	clusterGroup.Use(middlewares...)

	clusterGroup.GET("/version_matrix", getVersionMatrix(deps))
	clusterGroup.GET("/available_versions", getAvailableClusterVersions(deps))
	clusterGroup.GET("/upgrade_preflight", getClusterUpgradePreflight(deps))
}
