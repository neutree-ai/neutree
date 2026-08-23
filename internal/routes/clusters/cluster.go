package clusters

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ReleaseInfoProvider is the narrow policy dependency needed by cluster API
// operations that select versions from the current control-plane baseline.
type ReleaseInfoProvider interface {
	Current() (*v1.ReleaseInfo, error)
}

type Dependencies struct {
	Storage             storage.Storage
	ReleaseInfoProvider ReleaseInfoProvider
	ImageService        registry.ImageService
}

func RegisterClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	clusterGroup := group.Group("/clusters")
	clusterGroup.Use(middlewares...)

	clusterGroup.GET("/available_versions", getAvailableClusterVersions(deps))
}
