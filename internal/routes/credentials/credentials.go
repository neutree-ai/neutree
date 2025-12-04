package credentials

import (
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
	credGroup.GET("/clusters",
		middleware.RequirePermission("cluster:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "clusters"))

	// Image registry credentials (username, password, token)
	credGroup.GET("/image_registries",
		middleware.RequirePermission("image_registry:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "image_registries"))

	// Model registry credentials
	credGroup.GET("/model_registries",
		middleware.RequirePermission("model_registry:read-credentials", middleware.PermissionDependencies{
			Storage: deps.Storage,
		}),
		handleResourceCredentials(proxyDeps, "model_registries"))
}

func handleResourceCredentials(deps *proxies.Dependencies, tabelName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		proxyHandler := proxies.CreateProxyHandler(deps.StorageAccessURL, tabelName, proxies.CreatePostgrestAuthModifier(c))
		proxyHandler(c)
	}
}
