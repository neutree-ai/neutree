package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/admission"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var imageRegistryAdmissionResource = admission.NewResource[v1.ImageRegistry](storage.IMAGE_REGISTRY_TABLE)

func validateImageRegistryDeleteDependencies(s storage.Storage, candidate v1.ImageRegistry) error {
	workspace, name := candidate.GetWorkspace(), candidate.GetName()
	count, err := s.Count(storage.CLUSTERS_TABLE, []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
		{Column: "spec->>image_registry", Operator: "eq", Value: name},
	})
	if err != nil {
		return fmt.Errorf("failed to count clusters: %w", err)
	}
	if count > 0 {
		return newLegacyDeleteDependencyError(
			10127,
			fmt.Sprintf("cannot delete image_registry '%s/%s'", workspace, name),
			fmt.Sprintf("%d cluster(s) still reference this image registry", count),
		)
	}
	return nil
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
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid image registry url: %v", err)})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

func RegisterImageRegistryRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) error {
	proxyGroup := group.Group("/image_registries")
	proxyGroup.Use(middlewares...)
	if err := registerImageRegistryAdmission(deps); err != nil {
		return err
	}
	var createRunner, patchRunner gin.HandlerFunc
	if deps != nil && deps.Admission != nil {
		createRunner = CreateAdmissionRunnerWithOptions(deps.Admission, imageRegistryAdmissionResource, legacyCreateAdmissionRunnerOptions)
		patchRunner = CreatePatchAdmissionRunner(deps, storage.IMAGE_REGISTRY_TABLE, imageRegistryAdmissionResource)
	}
	handler := CreateStructProxyHandler[v1.ImageRegistry](deps, storage.IMAGE_REGISTRY_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", append([]gin.HandlerFunc{validateImageRegistryURL()}, withAdmissionRunner(createRunner, handler)...)...)
	proxyGroup.PATCH("", append(withAdmissionRunner(patchRunner, validateImageRegistryURL()), handler)...)
	return nil
}

func registerImageRegistryAdmission(deps *Dependencies) error {
	if deps == nil || deps.Admission == nil {
		return nil
	}
	if err := deps.Admission.RegisterResource(imageRegistryAdmissionResource); err != nil {
		return err
	}
	return deps.Admission.RegisterHook(imageRegistryAdmissionResource, admission.ValidateDelete(
		admission.HookMeta{Name: "community.image-registry.dependencies.delete", Order: 10}, 10127,
		func(_ admission.RequestContext, _, candidate v1.ImageRegistry) error {
			return validateImageRegistryDeleteDependencies(deps.Storage, candidate)
		},
	))
}
