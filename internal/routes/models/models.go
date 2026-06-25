package models

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/internal/model_registry/bentoml"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Dependencies defines the dependencies for model handlers
type Dependencies struct {
	Storage     storage.Storage
	TempDirFunc func() (string, error) // Function to get a temporary directory
	AuthConfig  middleware.AuthConfig
}

// progressWriter implements io.Writer for progress reporting via HTTP chunked encoding
type progressWriter struct {
	writer    io.Writer
	ctx       *gin.Context
	sent      int64
	totalSize int64
}

type progressLogWriter struct {
	lastPercent int64
}

func (pl *progressLogWriter) Write(p []byte) (int, error) {
	trimmed := strings.TrimSpace(string(p))
	if trimmed == "" {
		return len(p), nil
	}

	if percent, err := strconv.ParseFloat(trimmed, 64); err == nil {
		current := int64(percent)
		if current != pl.lastPercent {
			pl.lastPercent = current
			klog.V(4).Infof("Importing model... %d%%", current)
		}

		return len(p), nil
	}

	klog.V(4).Infof("Import progress: %s", trimmed)

	return len(p), nil
}

func (pw *progressWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	pw.sent += int64(n)

	// Write progress information only if total size is available
	if pw.totalSize > 0 {
		percentage := float64(pw.sent) / float64(pw.totalSize) * 100
		fmt.Fprintf(pw.writer, "%.2f\n", percentage)
	}

	// Flush the response
	if flusher, ok := pw.writer.(http.Flusher); ok {
		flusher.Flush()
	}

	return n, nil
}

func RegisterModelsRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	// Set default temporary directory function if not provided
	if deps.TempDirFunc == nil {
		deps.TempDirFunc = func() (string, error) {
			tempDir := filepath.Join(os.TempDir(), "neutree-models")
			if err := os.MkdirAll(tempDir, 0755); err != nil {
				return "", fmt.Errorf("failed to create temporary directory: %w", err)
			}

			return tempDir, nil
		}
	}

	permissionDeps := middleware.PermissionDependencies{
		Storage: deps.Storage,
	}
	// Workspace-scoped model registry routes with authentication
	workspaces := group.Group("/workspaces/:workspace")
	workspaces.Use(middlewares...) // Apply JWT authentication
	{
		modelRegistries := workspaces.Group("/model_registries/:registry")
		{
			models := modelRegistries.Group("/models")
			{
				// List all models in a registry
				models.GET("",
					middleware.RequireWorkspacePermission("model:read", permissionDeps),
					listModels(deps))

				// Get a specific model
				models.GET("/:model",
					middleware.RequireWorkspacePermission("model:read", permissionDeps),
					getModel(deps))

				// Upload a new model
				models.POST("",
					middleware.RequirePermission("model:push", permissionDeps),
					uploadModel(deps))

				// Finalize a direct model push after the client writes to shared storage
				models.POST("/:model/finalize",
					middleware.RequireWorkspacePermission("model:push", permissionDeps),
					finalizeModel(deps))

				// Download a model
				models.GET("/:model/download",
					middleware.RequirePermission("model:pull", permissionDeps),
					downloadModel(deps))

				// Delete a model
				models.DELETE("/:model",
					middleware.RequireWorkspacePermission("model:delete", permissionDeps),
					deleteModel(deps))
			}
		}
	}
}

type finalizeModelRequest struct {
	Version      string `json:"version"`
	CreationTime string `json:"creation_time,omitempty"`
	Size         string `json:"size,omitempty"`
	Module       string `json:"module,omitempty"`
}

func finalizeModel(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := strings.ToLower(c.Param("model"))

		var req finalizeModelRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Invalid finalize request",
			})

			return
		}

		req.Version = strings.TrimSpace(req.Version)
		if req.Version == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Version is required",
			})

			return
		}

		version := strings.ToLower(req.Version)
		if version == v1.LatestVersion {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Cannot use 'latest' as version, please specify a concrete version",
			})

			return
		}

		modelRegistry, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer (*modelRegistry).Disconnect() //nolint:errcheck

		modelVersion, err := (*modelRegistry).GetModelVersion(modelName, version)
		if err != nil {
			klog.Errorf("Failed to finalize model %s:%s: %v", modelName, version, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("Failed to finalize model %s:%s: %v", modelName, version, err),
			})

			return
		}

		if err := validateFinalizedModelVersion(req, modelVersion); err != nil {
			c.JSON(http.StatusConflict, gin.H{
				"message": err.Error(),
			})

			return
		}

		c.Status(http.StatusNoContent)
		c.Writer.WriteHeaderNow()
	}
}

func validateFinalizedModelVersion(req finalizeModelRequest, modelVersion *v1.ModelVersion) error {
	if modelVersion == nil {
		return fmt.Errorf("model version is missing after direct push")
	}

	if req.CreationTime != "" && modelVersion.CreationTime != req.CreationTime {
		return fmt.Errorf("direct push creation_time does not match registry metadata")
	}
	if req.Size != "" && modelVersion.Size != req.Size {
		return fmt.Errorf("direct push size does not match registry metadata")
	}
	if req.Module != "" && modelVersion.Module != req.Module {
		return fmt.Errorf("direct push module does not match registry metadata")
	}

	return nil
}

// getModelRegistry retrieves and connects to a model registry
func getModelRegistry(c *gin.Context, deps *Dependencies) (*model_registry.ModelRegistry, error) {
	workspace := c.Param("workspace")
	registryName := c.Param("registry")

	// Get model registry from storage
	modelRegistries, err := deps.Storage.ListModelRegistry(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(registryName),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find model registry: %w", err)
	}

	if len(modelRegistries) == 0 {
		return nil, fmt.Errorf("model registry not found: %s/%s", workspace, registryName)
	}

	// Create model registry client
	modelRegistry, err := model_registry.NewModelRegistry(&modelRegistries[0])
	if err != nil {
		return nil, fmt.Errorf("failed to create model registry client: %w", err)
	}

	// Connect to the registry
	if err := modelRegistry.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to model registry: %w", err)
	}

	return &modelRegistry, nil
}

// listModels handles listing all models in a registry
func listModels(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		search := c.Query("search")
		limit := 0

		if limitStr := c.Query("limit"); limitStr != "" {
			var err error

			limit, err = strconv.Atoi(limitStr)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"message": "Invalid limit parameter",
				})

				return
			}
		}

		// Get and connect to the model registry
		modelRegistry, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer (*modelRegistry).Disconnect() //nolint:errcheck

		// List models
		models, err := (*modelRegistry).ListModels(model_registry.ListOption{
			Search: search,
			Limit:  limit,
		})
		if err != nil {
			klog.Errorf("Failed to list models: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("Failed to list models: %v", err),
			})

			return
		}

		c.JSON(http.StatusOK, models)
	}
}

// getModel handles retrieving a specific model
func getModel(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := c.Param("model")

		version := c.Query("version")
		if version == "" {
			version = v1.LatestVersion
		}

		// Get and connect to the model registry
		modelRegistry, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer (*modelRegistry).Disconnect() //nolint:errcheck

		// Get model version details
		modelVersion, err := (*modelRegistry).GetModelVersion(modelName, version)
		if err != nil {
			klog.Errorf("Failed to get model %s:%s: %v", modelName, version, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("Failed to get model %s:%s: %v", modelName, version, err),
			})

			return
		}

		c.JSON(http.StatusOK, modelVersion)
	}
}

// uploadModel handles uploading a new model
func uploadModel(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		mr, err := c.Request.MultipartReader()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Invalid multipart form data",
			})

			return
		}

		var (
			name      string
			version   string
			modelSize int64 = -1
			modelPart *multipart.Part
		)

	readParts:
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{
					"message": "Failed to read multipart data",
				})

				return
			}

			switch part.FormName() {
			case "name":
				value, _ := io.ReadAll(part)
				name = strings.TrimSpace(string(value))
			case "version":
				value, _ := io.ReadAll(part)
				version = strings.TrimSpace(string(value))
			case "model_size":
				value, _ := io.ReadAll(part)
				if size, err := strconv.ParseInt(strings.TrimSpace(string(value)), 10, 64); err == nil {
					modelSize = size
				}
			case "model":
				modelPart = part
				break readParts
			default:
				_, _ = io.Copy(io.Discard, part)
			}

			part.Close()
		}

		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Model name is required",
			})

			return
		}

		if version == v1.LatestVersion {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "Cannot use 'latest' as version, please specify a concrete version or leave it empty for auto-generation",
			})

			return
		} else if version == "" {
			autoGenerateVersion, err := bentoml.GenerateVersion()
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"message": fmt.Sprintf("Failed to auto generate version: %v", err),
				})

				return
			}

			version = *autoGenerateVersion
		}

		// Get uploaded file
		if modelPart == nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"message": "No model file provided",
			})

			return
		}
		defer modelPart.Close()

		// Get and connect to the model registry
		modelRegistry, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer (*modelRegistry).Disconnect() //nolint:errcheck

		// Use chunked encoding for progress reporting
		c.Header("Transfer-Encoding", "chunked")
		c.Header("Content-Type", "text/plain")
		c.Status(http.StatusOK)

		// Create a progress writer that writes to the response
		progressWriter := &progressWriter{
			writer:    &progressLogWriter{},
			ctx:       c,
			totalSize: modelSize,
		}

		// Import directly from uploaded file reader with progress
		klog.V(4).Infof("Importing model %s:%s", name, version)

		if err := (*modelRegistry).ImportModel(modelPart, name, version, progressWriter); err != nil {
			klog.Errorf("Failed to import model: %v", err)
			fmt.Fprintf(c.Writer, "Error: Failed to import model: %v\n", err)

			return
		}

		// Finalize progress
		klog.V(4).Infof("Imported model %s:%s", name, version)
		fmt.Fprintf(c.Writer, "Success: Model imported successfully\n")
		c.Writer.Flush()
	}
}

// downloadModel handles downloading a model
func downloadModel(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := c.Param("model")

		version := c.Query("version")
		if version == "" {
			version = v1.LatestVersion
		}

		// Get temporary directory
		tempDir, err := deps.TempDirFunc()
		if err != nil {
			klog.Errorf("Failed to get temporary directory: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("Failed to prepare for download: %v", err),
			})

			return
		}

		// Get and connect to the model registry
		modelRegistry, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer (*modelRegistry).Disconnect() //nolint:errcheck

		// Create temporary file for export
		tempFile := filepath.Join(tempDir, fmt.Sprintf("%s-%s.bentomodel", modelName, version))
		defer os.Remove(tempFile) // Clean up when done

		// Export model to temporary file
		if err := (*modelRegistry).ExportModel(modelName, version, tempFile); err != nil {
			klog.Errorf("Failed to export model %s:%s: %v", modelName, version, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("Failed to export model: %v", err),
			})

			return
		}

		// Set response headers
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s-%s.bentomodel", modelName, version))
		c.Header("Content-Type", "application/octet-stream")

		// Serve the file
		c.File(tempFile)
	}
}

// deleteModel handles deleting a model
func deleteModel(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := c.Param("model")

		version := c.Query("version")
		if version == "" {
			version = v1.LatestVersion
		}

		// Get and connect to the model registry
		modelRegistry, err := getModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}
		defer (*modelRegistry).Disconnect() //nolint:errcheck

		// Delete the model
		if err := (*modelRegistry).DeleteModel(modelName, version); err != nil {
			klog.Errorf("Failed to delete model %s:%s: %v", modelName, version, err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": fmt.Sprintf("Failed to delete model: %v", err),
			})

			return
		}

		c.Status(http.StatusNoContent)
	}
}
