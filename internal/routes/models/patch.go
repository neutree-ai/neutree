package models

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

// patchModelRequest is the editable surface of a model. Everything here is a
// label on top of the model; the physical name, version and storage path are not
// editable and are not represented.
type patchModelRequest struct {
	// Alias is the display name. A nil value leaves the current alias alone; an
	// empty string removes it.
	Alias *string `json:"alias,omitempty"`
	// Info is the complete set of hand-filled model info fields. It replaces any
	// previously recorded set wholesale — nil leaves it alone, an empty object
	// clears it — because per-field patching would have no way to express
	// "remove this one value".
	Info *v1.ModelInfo `json:"info,omitempty"`
}

// patchModel updates a model's alias and hand-filled metadata.
func patchModel(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := c.Param("model")

		version := c.Query("version")
		if version == "" {
			version = v1.LatestVersion
		}

		var req patchModelRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Invalid model update request",
			})

			return
		}

		handle, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer handle.client.Disconnect() //nolint:errcheck

		// Resolve the model first: it validates that the model is there, turns
		// "latest" into the version the alias row has to name, and tells us
		// straight away whether this registry supports being written to at all.
		detail, err := handle.client.GetModelDetail(modelName, version)
		if err != nil {
			respondModelLookupError(c, modelName, version, err)

			return
		}

		if !applyModelPatch(c, deps, handle, modelName, detail.Name, req) {
			return
		}

		// Read back rather than patching the copy in hand, so the response is what
		// the next GET will return.
		updated, err := handle.client.GetModelDetail(modelName, detail.Name)
		if err != nil {
			respondModelLookupError(c, modelName, detail.Name, err)

			return
		}

		if err := attachModelAlias(deps, handle, modelName, updated); err != nil {
			klog.Errorf("Failed to read alias of model %s:%s: %v", modelName, detail.Name, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}

		c.JSON(http.StatusOK, updated)
	}
}

// applyModelPatch performs the requested writes, answering the request itself on
// any failure. It reports whether the caller should carry on and respond.
func applyModelPatch(c *gin.Context, deps *Dependencies, handle *registryHandle,
	modelName, version string, req patchModelRequest) bool {
	if req.Info != nil {
		// Provenance is the server's to state, not the client's: whatever arrives
		// here was typed by a person, so it is recorded as manual regardless of
		// what the request claimed.
		manual := &v1.ModelInfo{}
		manual.MergeManual(req.Info)

		if err := handle.client.SetManualModelInfo(modelName, version, manual); err != nil {
			respondModelWriteError(c, modelName, version, err)

			return false
		}
	}

	if req.Alias == nil {
		return true
	}

	status, body, err := setModelAlias(deps, handle, modelName, version, *req.Alias)
	if err != nil {
		klog.Errorf("Failed to set alias of model %s:%s: %v", modelName, version, err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"message": err.Error(),
		})

		return false
	}

	if status != 0 {
		c.JSON(status, body)

		return false
	}

	return true
}

func respondModelLookupError(c *gin.Context, modelName, version string, err error) {
	if errors.Is(err, model_registry.ErrNotSupported) {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "This model registry does not support editing model metadata",
		})

		return
	}

	klog.Errorf("Failed to get model %s:%s: %v", modelName, version, err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"message": fmt.Sprintf("Failed to get model %s:%s: %v", modelName, version, err),
	})
}

func respondModelWriteError(c *gin.Context, modelName, version string, err error) {
	if errors.Is(err, model_registry.ErrNotSupported) {
		c.JSON(http.StatusBadRequest, gin.H{
			"message": "This model registry does not support editing model metadata",
		})

		return
	}

	klog.Errorf("Failed to update model %s:%s: %v", modelName, version, err)
	c.JSON(http.StatusInternalServerError, gin.H{
		"message": fmt.Sprintf("Failed to update model %s:%s: %v", modelName, version, err),
	})
}
