package proxies

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/recipe"
)

// RegisterModelCatalogRoutes registers model catalog routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
//
// Recipe catalogs are imported client-side (the UI parses YAML / fetches URLs
// in the browser and creates each document through the normal POST path), so
// there is no server-side import endpoint. The recipe validation middleware
// below is the single server-side gate that rejects structurally invalid
// recipe specs before they reach storage.
func RegisterModelCatalogRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/model_catalogs")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ModelCatalog](deps, "model_catalogs")
	recipeValidation := validateModelCatalogRecipe()

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", recipeValidation, handler)
	proxyGroup.PATCH("", recipeValidation, handler)
}

// validateModelCatalogRecipe rejects structurally invalid recipe catalogs at
// the data-entry boundary. Writes go through PostgREST (RLS-scoped to the
// caller's JWT), which never runs recipe validation on its own, so this
// middleware is where the recipe invariants in recipe.ValidateModelCatalogSpec
// are actually enforced — otherwise an invalid recipe persists and only fails
// later inside the endpoint controller at deploy time.
func validateModelCatalogRecipe() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost && c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body: " + err.Error()})
			c.Abort()

			return
		}

		// Restore the body so the downstream proxy handler can re-read it.
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			c.Next()
			return
		}

		// PostgREST accepts both a single object and an array (bulk insert).
		var catalogs []v1.ModelCatalog

		if trimmed[0] == '[' {
			if err := json.Unmarshal(trimmed, &catalogs); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model_catalog payload: " + err.Error()})
				c.Abort()

				return
			}
		} else {
			var one v1.ModelCatalog
			if err := json.Unmarshal(trimmed, &one); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model_catalog payload: " + err.Error()})
				c.Abort()

				return
			}

			catalogs = []v1.ModelCatalog{one}
		}

		for _, catalog := range catalogs {
			// A metadata-only PATCH carries no spec — nothing recipe-related to
			// validate.
			if catalog.Spec == nil {
				continue
			}

			if err := recipe.ValidateModelCatalogSpec(catalog.Spec); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				c.Abort()

				return
			}
		}

		c.Next()
	}
}
