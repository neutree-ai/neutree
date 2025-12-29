package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateImageRegistryDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		fmt.Println("validateImageRegistryDeletion", workspace, name)

		count, err := s.Count(storage.CLUSTERS_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "spec->>image_registry", Operator: "eq", Value: name},
		})
		if err != nil {
			return fmt.Errorf("failed to count clusters: %w", err)
		}

		if count > 0 {
			return &middleware.DeletionError{
				Code:    "10127",
				Message: fmt.Sprintf("cannot delete image_registry '%s/%s'", workspace, name),
				Hint:    fmt.Sprintf("%d cluster(s) still reference this image registry", count),
			}
		}

		return nil
	}
}

func RegisterImageRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/image_registries")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.IMAGE_REGISTRY_TABLE,
		validateImageRegistryDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.ImageRegistry](deps, storage.IMAGE_REGISTRY_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
}
