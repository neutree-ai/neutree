package models

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Storage storage.Storage
}

func RegisterRoutes(r *gin.Engine, deps *Dependencies) {
	r.GET("/api/v1/search-models/:name", searchModels(deps))
}

func searchModels(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		registryName := c.Param("name")
		search := c.Query("search")

		modelRegistries, err := deps.Storage.ListModelRegistry(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(registryName),
				},
			},
		})
		if err != nil {
			errS := fmt.Sprintf("Failed to list model registries: %v", err)
			klog.Errorf(errS)

			c.JSON(http.StatusInternalServerError, gin.H{
				"message": errS,
			})
			return
		}

		if len(modelRegistries) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"message": "model registry not found",
			})
			return
		}

		registry, err := model_registry.NewModelRegistry(&modelRegistries[0])
		if err != nil {
			errS := fmt.Sprintf("Failed to create model registry: %v", err)
			klog.Errorf(errS)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": errS,
			})
			return
		}

		models, err := registry.ListModels(model_registry.ListOption{
			Search: search,
		})
		if err != nil {
			errS := fmt.Sprintf("Failed to list models: %v", err)
			klog.Errorf(errS)

			c.JSON(http.StatusInternalServerError, gin.H{
				"message": errS,
			})
			return
		}

		c.JSON(http.StatusOK, models)
	}
}
