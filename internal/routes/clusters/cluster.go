package clusters

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ReleaseInfoProvider is the narrow policy dependency needed for cluster
// package Profile imports.
type ReleaseInfoProvider = middleware.ReleaseInfoProvider

type Dependencies struct {
	Storage             storage.Storage
	ReleaseInfoProvider ReleaseInfoProvider
	ImageService        registry.ImageService
}

func RegisterClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	clusterGroup := group.Group("/clusters")
	clusterGroup.Use(middlewares...)

	clusterGroup.GET("/available_versions", getAvailableClusterVersions(deps))
	clusterGroup.POST("/profile_upsert",
		middleware.RequirePermission("system:admin", middleware.PermissionDependencies{Storage: deps.Storage}),
		middleware.ClusterProfileImportValidation(deps.ReleaseInfoProvider),
		upsertClusterProfile(deps))
}
