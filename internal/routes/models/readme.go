package models

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

const (
	// readmeContentType is markdown, not HTML. The server returns what the registry
	// stores and renders nothing: rendering it here would run whatever it contains
	// in every client that displays the result.
	readmeContentType = "text/markdown; charset=utf-8"
	// readmeTruncatedHeader marks a card cut at model_registry.MaxReadmeBytes, so a
	// client can tell it from one that simply ends there.
	readmeTruncatedHeader = "X-Neutree-Content-Truncated"
)

// getModelReadme serves a model's README as stored.
func getModelReadme(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := c.Param("model")

		version := c.Query("version")
		if version == "" {
			version = v1.LatestVersion
		}

		handle, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
				"reason":  reasonUnavailable,
			})

			return
		}
		defer handle.client.Disconnect() //nolint:errcheck

		readme, err := handle.client.GetReadme(modelName, version)
		if err != nil {
			respondReadmeError(c, modelName, version, err)

			return
		}

		if readme.Truncated {
			c.Header(readmeTruncatedHeader, "true")
		}

		c.Data(http.StatusOK, readmeContentType, []byte(readme.Content))
	}
}

func respondReadmeError(c *gin.Context, modelName, version string, err error) {
	context := fmt.Sprintf("Failed to read README of model %s:%s", modelName, version)

	if errors.Is(err, model_registry.ErrNotFound) {
		// Said in the model's own terms: "has no README" is a fact about the model,
		// not something the user has to act on.
		c.JSON(http.StatusNotFound, gin.H{
			"message": fmt.Sprintf("Model %s:%s has no README", modelName, version),
			"reason":  reasonNotFound,
		})

		return
	}

	if respondRegistryError(c, context, err) {
		return
	}

	klog.Errorf("%s: %v", context, err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"message": fmt.Sprintf("%s: %v", context, err),
		"reason":  reasonUnavailable,
	})
}
