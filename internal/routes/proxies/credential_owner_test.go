package proxies

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
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

func TestStampCredentialOwnerLabelAbortsOnMalformedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var nextCalled bool

	router := gin.New()
	router.POST("/resource", func(c *gin.Context) {
		c.Set("user_id", "owner-user")
	}, StampCredentialOwnerLabel(), func(c *gin.Context) {
		nextCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/resource", strings.NewReader(`{"metadata": "not-an-object"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, nextCalled, "downstream handler must not run when label rewriting fails")
}

func TestCredentialOwnerFilter(t *testing.T) {
	filter := CredentialOwnerFilter("owner-user")

	assert.Equal(t, "metadata->labels->>neutree.ai/credential-owner", filter.Column)
	assert.Equal(t, "eq", filter.Operator)
	assert.Equal(t, "owner-user", filter.Value)
}

func stubGenericQueryLabels(mockStorage *storageMocks.MockStorage, labels map[string]string) {
	mockStorage.On("GenericQuery", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			result := args.Get(3)
			resultValue := reflect.ValueOf(result).Elem()

			row := map[string]interface{}{"metadata": map[string]interface{}{"labels": labels}}

			data, err := json.Marshal([]map[string]interface{}{row})
			if err != nil {
				panic(err)
			}

			decoded := reflect.New(resultValue.Type())
			if err := json.Unmarshal(data, decoded.Interface()); err != nil {
				panic(err)
			}

			resultValue.Set(decoded.Elem())
		}).
		Return(nil)
}

func TestPinCredentialOwnerLabelRestoresExistingOwnerOnPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := storageMocks.NewMockStorage(t)
	stubGenericQueryLabels(mockStorage, map[string]string{
		v1.LabelCredentialOwner: "real-owner",
		"team":                  "platform",
	})

	router := gin.New()
	router.PATCH("/resource", PinCredentialOwnerLabel(&Dependencies{Storage: mockStorage}, "external_endpoints"),
		func(c *gin.Context) {
			var payload map[string]interface{}
			assert.NoError(t, json.NewDecoder(c.Request.Body).Decode(&payload))
			c.JSON(http.StatusOK, payload)
		})

	req := httptest.NewRequest(http.MethodPatch, "/resource?metadata-%3Ename=eq.foo", strings.NewReader(`{
		"metadata": {
			"labels": {
				"team": "platform",
				"neutree.ai/credential-owner": "attacker"
			}
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var payload map[string]interface{}
	assert.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))

	metadata := payload["metadata"].(map[string]interface{})
	labels := metadata["labels"].(map[string]interface{})
	assert.Equal(t, "real-owner", labels[v1.LabelCredentialOwner])
	assert.Equal(t, "platform", labels["team"])
}

func TestPinCredentialOwnerLabelPassesThroughWhenResourceNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := storageMocks.NewMockStorage(t)
	mockStorage.On("GenericQuery", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	router := gin.New()
	router.PATCH("/resource", PinCredentialOwnerLabel(&Dependencies{Storage: mockStorage}, "external_endpoints"),
		func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		})

	req := httptest.NewRequest(http.MethodPatch, "/resource?metadata-%3Ename=eq.foo", strings.NewReader(`{"metadata":{"labels":{"neutree.ai/credential-owner":"attacker"}}}`))
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}
