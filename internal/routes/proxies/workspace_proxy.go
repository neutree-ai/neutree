package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateWorkspaceDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		counts := make(map[string]int)
		tables := []string{
			storage.ENDPOINT_TABLE,
			storage.CLUSTERS_TABLE,
			storage.MODEL_REGISTRY_TABLE,
			storage.IMAGE_REGISTRY_TABLE,
			storage.MODEL_CATALOG_TABLE,
			storage.ROLE_TABLE,
			storage.API_KEY_TABLE,
		}

		for _, table := range tables {
			count, err := s.Count(table, []storage.Filter{
				{Column: "metadata->>workspace", Operator: "eq", Value: name},
			})
			if err != nil {
				return fmt.Errorf("failed to count %s: %w", table, err)
			}

			counts[table] = count
		}

		count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
			{Column: "spec->>workspace", Operator: "eq", Value: name},
		})
		if err != nil {
			return fmt.Errorf("failed to count role assignments: %w", err)
		}

		counts[storage.ROLE_ASSIGNMENT_TABLE] = count

		totalCount := 0
		for _, count := range counts {
			totalCount += count
		}

		if totalCount > 0 {
			hint := "Resources still exist in this workspace:"

			for resourceType, count := range counts {
				if count > 0 {
					hint += fmt.Sprintf("\n- %s: %d", resourceType, count)
				}
			}

			return &middleware.DeletionError{
				Code:    "10125",
				Message: fmt.Sprintf("cannot delete workspace '%s'", name),
				Hint:    hint,
			}
		}

		return nil
	}
}

func RegisterWorkspaceRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/workspaces")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.WORKSPACE_TABLE,
		validateWorkspaceDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.Workspace](deps, storage.WORKSPACE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
}
