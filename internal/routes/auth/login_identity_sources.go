package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"
)

// handleListLoginIdentitySources answers GET /auth/identity-sources for the
// login page, which has no session yet: the enabled identity sources, each with
// its name, display name and type and nothing else.
func handleListLoginIdentitySources(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		sources, err := deps.Storage.ListLoginIdentitySources()
		if err != nil {
			klog.Errorf("Failed to list identity sources for the login page: %v", err)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "identity sources are unavailable"})

			return
		}

		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, sources)
	}
}
