package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestRequirePermission_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := mocks.NewMockStorage(t)
	mockStorage.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "user-123" &&
			params["required_permission"] == "user_profile:create" &&
			params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = true
	}).Return(nil)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", "user-123")
		c.Next()
	})
	router.GET("/test", RequirePermission("user_profile:create", PermissionDependencies{
		Storage: mockStorage,
	}), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestRequirePermission_Forbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := mocks.NewMockStorage(t)
	mockStorage.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = false
	}).Return(nil)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", "user-123")
		c.Next()
	})
	router.GET("/test", RequirePermission("user_profile:create", PermissionDependencies{
		Storage: mockStorage,
	}), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestRequirePermission_NoUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := mocks.NewMockStorage(t)

	router := gin.New()
	router.GET("/test", RequirePermission("user_profile:create", PermissionDependencies{
		Storage: mockStorage,
	}), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestRequireWorkspacePermission(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := mocks.NewMockStorage(t)
	mockStorage.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "user-123" &&
			params["required_permission"] == "workspace:read" &&
			params["workspace"] == "workspace-abc"
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = true
	}).Return(nil)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", "user-123")
		c.Next()
	})
	router.GET("/workspaces/:workspace/resource", RequireWorkspacePermission("workspace:read", PermissionDependencies{
		Storage: mockStorage,
	}), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/workspaces/workspace-abc/resource", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestRequireWorkspacePermission_NoWorkspaceParam(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := mocks.NewMockStorage(t)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", "user-123")
		c.Next()
	})
	router.GET("/workspaces/:workspace/resource", RequireWorkspacePermission("workspace:read", PermissionDependencies{
		Storage: mockStorage,
	}), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/workspaces//resource", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestRequireWorkspacePermission_Forbidden(t *testing.T) {
	gin.SetMode(gin.TestMode)

	mockStorage := mocks.NewMockStorage(t)
	mockStorage.On("CallDatabaseFunction", "has_permission", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = false
	}).Return(nil)

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", "user-123")
		c.Next()
	})
	router.GET("/workspaces/:workspace/resource", RequireWorkspacePermission("workspace:read", PermissionDependencies{
		Storage: mockStorage,
	}), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	req, _ := http.NewRequest("GET", "/workspaces/workspace-abc/resource", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	mockStorage.AssertExpectations(t)
}
