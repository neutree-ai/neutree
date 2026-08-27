// Package images serves what an image registry holds, as the models package
// serves what a model registry holds. The registry records themselves are CRUD
// through the PostgREST proxy; these are the reads that have to talk to the
// registry itself.
package images

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Storage      storage.Storage
	ImageService registry.ImageService
	// RepositoryService enumerates repositories, which -- unlike tags -- has no
	// portable answer and so is a separate service speaking each registry's own
	// dialect.
	RepositoryService registry.RepositoryService
}

// RegisterImageRoutes registers the image registry content routes.
//
// Shaped like the model registry's: workspace and registry in the path, because
// what a registry holds is only meaningful within the workspace that can see it.
// A repository name containing a slash is percent-encoded by the caller, which
// the router preserves — the same thing model names rely on (see
// routes.ConfigureEngine).
func RegisterImageRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	workspaces := group.Group("/workspaces/:workspace")
	workspaces.Use(middlewares...)

	registries := workspaces.Group("/image_registries/:registry")
	registries.GET("/repositories", listImageRepositories(deps))
	registries.GET("/repositories/:repository/tags", getImageTags(deps))
}
