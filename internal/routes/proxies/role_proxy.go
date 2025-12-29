package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateRoleDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		filters := []storage.Filter{
			{Column: "spec->>role", Operator: "eq", Value: name},
		}

		if workspace == "" {
			filters = append(filters, storage.Filter{
				Column:   "metadata->>workspace",
				Operator: "is",
				Value:    "null",
			})
		} else {
			filters = append(filters, storage.Filter{
				Column:   "metadata->>workspace",
				Operator: "eq",
				Value:    workspace,
			})
		}

		count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, filters)
		if err != nil {
			return fmt.Errorf("failed to count role assignments: %w", err)
		}

		if count > 0 {
			displayWorkspace := workspace
			if displayWorkspace == "" {
				displayWorkspace = "global"
			}

			return &middleware.DeletionError{
				Code:    "10129",
				Message: fmt.Sprintf("cannot delete role '%s/%s'", displayWorkspace, name),
				Hint:    fmt.Sprintf("%d role assignment(s) still reference this role", count),
			}
		}

		return nil
	}
}

func RegisterRoleRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/roles")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.ROLE_TABLE,
		validateRoleDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.Role](deps, storage.ROLE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
}
