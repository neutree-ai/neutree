package proxies

import (
	"fmt"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateUserProfileDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, userID string) error {
		count, err := s.Count(storage.ROLE_ASSIGNMENT_TABLE, []storage.Filter{
			{Column: "spec->>user_id", Operator: "eq", Value: userID},
		})
		if err != nil {
			return fmt.Errorf("failed to count role assignments: %w", err)
		}

		if count > 0 {
			return &middleware.DeletionError{
				Code:    "10130",
				Message: fmt.Sprintf("cannot delete user_profile '%s'", userID),
				Hint:    fmt.Sprintf("%d role assignment(s) still reference this user", count),
			}
		}

		return nil
	}
}

func RegisterUserProfileRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/user_profiles")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.USER_PROFILE_TABLE,
		validateUserProfileDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.UserProfile](deps, storage.USER_PROFILE_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
}
