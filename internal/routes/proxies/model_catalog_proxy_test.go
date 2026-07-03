package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// newRecipeValidationRouter mounts the middleware in front of a stub handler so
// tests can observe both the rejection responses and the pass-through path.
func newRecipeValidationRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/model_catalogs", validateModelCatalogRecipe(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"proxied": true})
	})

	return r
}

func TestValidateModelCatalogRecipe(t *testing.T) {
	validRecipe := `{
		"api_version": "v1",
		"kind": "ModelCatalog",
		"metadata": {"name": "mc", "workspace": "default"},
		"spec": {
			"engine": {"engine": "vllm", "version": "v0.11.2"},
			"variants": {
				"default": {"model": {"registry": "hf", "name": "org/model", "task": "text-generation"}}
			}
		}
	}`

	tests := []struct {
		name              string
		body              string
		expectStatus      int
		expectCode        string
		expectMessagePart string
	}{
		{
			name:         "valid recipe passes through",
			body:         validRecipe,
			expectStatus: http.StatusOK,
		},
		{
			name:         "valid recipe in bulk array passes through",
			body:         "[" + validRecipe + "]",
			expectStatus: http.StatusOK,
		},
		{
			name:              "malformed json is rejected with the payload code",
			body:              `{"spec": {"resources": {"cpu": 1}}}`,
			expectStatus:      http.StatusBadRequest,
			expectCode:        "10223",
			expectMessagePart: "invalid model_catalog payload",
		},
		{
			name:              "malformed json array is rejected with the payload code",
			body:              `[{"spec": {"resources": {"cpu": 1}}}]`,
			expectStatus:      http.StatusBadRequest,
			expectCode:        "10223",
			expectMessagePart: "invalid model_catalog payload",
		},
		{
			name: "recipe invariant violation is rejected with the recipe code",
			body: `{
				"spec": {
					"variants": {"default": {"model": {"name": "org/model"}}},
					"features": [{"name": "a", "conflicts_with": ["a"]}]
				}
			}`,
			expectStatus:      http.StatusBadRequest,
			expectCode:        "10224",
			expectMessagePart: "lists itself in conflicts_with",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newRecipeValidationRouter()
			req := httptest.NewRequest(http.MethodPost, "/model_catalogs", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			assert.Equal(t, tt.expectStatus, w.Code)

			if tt.expectCode == "" {
				return
			}

			// Rejections use the PostgREST error shape the SPA's data provider
			// parses: {code, message, hint}.
			var resp validationError
			assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, tt.expectCode, resp.Code)
			assert.Contains(t, resp.Message, tt.expectMessagePart)
			assert.NotEmpty(t, resp.Hint)
		})
	}
}
