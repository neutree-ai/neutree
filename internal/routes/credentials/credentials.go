package credentials

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Dependencies defines the dependencies for credentials handlers
type Dependencies struct {
	Storage          storage.Storage
	StorageAccessURL string
}

// RegisterCredentialsRoutes registers credentials retrieval routes
// This is a separate API group to ensure explicit intent when accessing sensitive data
// For every resource, we will check the permission before return the sensitive data
// Now only support clusters, image registries, and model registries
func RegisterCredentialsRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	credGroup := group.Group("/credentials")
	credGroup.Use(middlewares...)

	proxyDeps := &proxies.Dependencies{
		Storage:          deps.Storage,
		StorageAccessURL: deps.StorageAccessURL,
	}

	// Cluster credentials (kubeconfig, SSH keys, etc.)
	// Shared infrastructure credential, visibility governed by RBAC only.
	credGroup.GET("/clusters",
		middleware.RequirePermission("cluster:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "clusters", false))

	// Image registry credentials (username, password, token)
	// Shared infrastructure credential, visibility governed by RBAC only.
	credGroup.GET("/image_registries",
		middleware.RequirePermission("image_registry:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "image_registries", false))

	// Model registry credentials
	// Shared infrastructure credential, visibility governed by RBAC only.
	credGroup.GET("/model_registries",
		middleware.RequirePermission("model_registry:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "model_registries", false))

	// External endpoint credentials (auth tokens)
	// Users bring their own third-party API keys here, so access is restricted
	// to the creating user even for callers holding external_endpoint:read-credentials.
	credGroup.GET("/external_endpoints",
		middleware.RequirePermission("external_endpoint:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "external_endpoints", true))
}

func handleResourceCredentials(deps *proxies.Dependencies, tabelName string, restrictToOwner bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if restrictToOwner {
			userID := c.GetString("user_id")
			if userID == "" {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
				c.Abort()

				return
			}

			c.Request.URL.RawQuery = proxies.AddCredentialOwnerQuery(c.Request.URL.Query(), userID).Encode()
		}

		proxyHandler := proxies.CreateProxyHandler(deps.StorageAccessURL, tabelName, proxies.CreatePostgrestAuthModifier(c))
		proxyHandler(c)
	}
}
