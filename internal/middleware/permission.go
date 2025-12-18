package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

type PermissionDependencies struct {
	Storage storage.Storage
}

func RequireWorkspacePermission(permission string, deps PermissionDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("user_id")
		if userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "user not authenticated",
			})
			c.Abort()

			return
		}

		workspace := c.Param("workspace")
		if workspace == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "workspace parameter is required",
			})
			c.Abort()

			return
		}

		hasPermission, err := checkPermission(deps.Storage, userID, workspace, permission)
		if err != nil {
			klog.Errorf("Failed to check permission %s for user %s: %v", permission, userID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to check permissions",
			})
			c.Abort()

			return
		}

		if !hasPermission {
			c.JSON(http.StatusForbidden, gin.H{
				"error":    "insufficient permissions",
				"required": permission,
			})
			c.Abort()

			return
		}

		c.Next()
	}
}

func RequirePermission(permission string, deps PermissionDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetString("user_id")
		if userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "user not authenticated",
			})
			c.Abort()

			return
		}

		hasPermission, err := checkPermission(deps.Storage, userID, "", permission)
		if err != nil {
			klog.Errorf("Failed to check permission %s for user %s: %v", permission, userID, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to check permissions",
			})
			c.Abort()

			return
		}

		if !hasPermission {
			c.JSON(http.StatusForbidden, gin.H{
				"error":    "insufficient permissions",
				"required": permission,
			})
			c.Abort()

			return
		}

		c.Next()
	}
}

func checkPermission(s storage.Storage, userID string, workspace, permission string) (bool, error) {
	var result bool

	params := map[string]interface{}{
		"user_uuid":           userID,
		"required_permission": permission,
	}

	if workspace != "" {
		params["workspace"] = workspace
	} else {
		params["workspace"] = nil
	}

	err := s.CallDatabaseFunction("has_permission", params, &result)
	if err != nil {
		return false, err
	}

	return result, nil
}
