package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateModelRegistryDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		count, err := s.Count(storage.ENDPOINT_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "spec->model->>registry", Operator: "eq", Value: name},
		})
		if err != nil {
			return fmt.Errorf("failed to count endpoints: %w", err)
		}

		if count > 0 {
			return &middleware.DeletionError{
				Code:    "10128",
				Message: fmt.Sprintf("cannot delete model_registry '%s/%s'", workspace, name),
				Hint:    fmt.Sprintf("%d endpoint(s) still reference this model registry", count),
			}
		}

		return nil
	}
}

func RegisterModelRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/model_registries")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.MODEL_REGISTRY_TABLE,
		validateModelRegistryDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.ModelRegistry](deps, storage.MODEL_REGISTRY_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
}
