package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/util"
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

func validateImageRegistryURL() gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read request body: %v", err)})
			c.Abort()

			return
		}

		c.Request.Body.Close()
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

		var imageRegistry v1.ImageRegistry
		if err := json.Unmarshal(body, &imageRegistry); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse image registry: %v", err)})
			c.Abort()

			return
		}

		if imageRegistry.Spec != nil && imageRegistry.Spec.URL != "" {
			if _, err := util.GetImageRegistryHost(&imageRegistry); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"error": fmt.Sprintf("invalid image registry url: %v", err),
				})
				c.Abort()

				return
			}
		}

		c.Next()
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
	proxyGroup.POST("", validateImageRegistryURL(), handler)
	proxyGroup.PATCH("", deletionValidation, validateImageRegistryURL(), handler)
}
