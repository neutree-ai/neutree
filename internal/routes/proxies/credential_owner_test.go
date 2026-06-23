package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestStampCredentialOwnerLabel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/resource", func(c *gin.Context) {
		c.Set("user_id", "owner-user")
	}, StampCredentialOwnerLabel(), func(c *gin.Context) {
		var payload map[string]interface{}
		assert.NoError(t, json.NewDecoder(c.Request.Body).Decode(&payload))
		c.JSON(http.StatusOK, payload)
	})

	req := httptest.NewRequest(http.MethodPost, "/resource", strings.NewReader(`{
		"metadata": {
			"name": "registry",
			"labels": {
				"team": "platform",
				"neutree.ai/credential-owner": "spoofed-user"
			}
		},
		"spec": {}
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var payload map[string]interface{}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))

	metadata := payload["metadata"].(map[string]interface{})
	labels := metadata["labels"].(map[string]interface{})
	assert.Equal(t, "platform", labels["team"])
	assert.Equal(t, "owner-user", labels[v1.LabelCredentialOwner])
}

func TestStampCredentialOwnerLabelRequiresUser(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	router.POST("/resource", StampCredentialOwnerLabel(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/resource", strings.NewReader(`{"metadata":{"name":"registry"}}`))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestCredentialOwnerFilter(t *testing.T) {
	filter := CredentialOwnerFilter("owner-user")

	assert.Equal(t, "metadata->labels->>neutree.ai/credential-owner", filter.Column)
	assert.Equal(t, "eq", filter.Operator)
	assert.Equal(t, "owner-user", filter.Value)
}
