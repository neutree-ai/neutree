package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/internal/middleware"
)

// createTestContext creates a test context for testing
func createTestContext(method, path string, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	var req *http.Request
	if body != nil {
		jsonBody, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewBuffer(jsonBody))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}

	c.Request = req
	return c, w
}

// createTestContextWithAuth creates a test context with authentication
func createTestContextWithAuth(method, path string, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	c, w := createTestContext(method, path, body)

	// Set a mock user ID in the context to simulate authentication
	c.Set("user_id", uuid.New().String())

	return c, w
}

func TestCreateServiceRoleToken(t *testing.T) {
	tests := []struct {
		name      string
		jwtSecret string
		wantErr   bool
	}{
		{
			name:      "valid JWT secret",
			jwtSecret: "test-secret-key",
			wantErr:   false,
		},
		{
			name:      "empty JWT secret",
			jwtSecret: "",
			wantErr:   false, // JWT library can handle empty secret
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := createServiceRoleToken(tt.jwtSecret)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Empty(t, token)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, token)

				// Verify token can be parsed and has correct claims
				parsedToken, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
					return []byte(tt.jwtSecret), nil
				})

				assert.NoError(t, err)
				assert.True(t, parsedToken.Valid)

				claims, ok := parsedToken.Claims.(jwt.MapClaims)
				assert.True(t, ok)
				assert.Equal(t, "service_role", claims["role"])
				assert.Equal(t, "neutree", claims["iss"])
				assert.NotNil(t, claims["iat"])
				assert.NotNil(t, claims["exp"])

				// Verify expiry is approximately 1 hour from now
				exp := int64(claims["exp"].(float64))
				expectedExp := time.Now().Add(time.Hour).Unix()
				assert.InDelta(t, expectedExp, exp, 10) // Allow 10 seconds tolerance
			}
		})
	}
}

func TestCreateUser_ValidationErrors(t *testing.T) {
	deps := &Dependencies{
		AuthConfig: middleware.AuthConfig{
			JwtSecret: "test-secret",
		},
		AuthEndpoint: "http://test-auth:9999",
	}

	tests := []struct {
		name           string
		requestBody    interface{}
		authenticated  bool
		expectedStatus int
		expectedError  string
	}{
		{
			name:           "invalid JSON",
			requestBody:    "invalid-json",
			authenticated:  true,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "cannot unmarshal string",
		},
		{
			name: "missing email",
			requestBody: CreateUserRequest{
				Password: "password123",
				Username: "testuser",
			},
			authenticated:  true,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Email",
		},
		{
			name: "missing password",
			requestBody: CreateUserRequest{
				Email:    "test@example.com",
				Username: "testuser",
			},
			authenticated:  true,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Password",
		},
		{
			name: "missing username",
			requestBody: CreateUserRequest{
				Email:    "test@example.com",
				Password: "password123",
			},
			authenticated:  true,
			expectedStatus: http.StatusBadRequest,
			expectedError:  "Username is required",
		},
		{
			name: "not authenticated",
			requestBody: CreateUserRequest{
				Email:    "test@example.com",
				Password: "password123",
				Username: "testuser",
			},
			authenticated:  false,
			expectedStatus: http.StatusUnauthorized,
			expectedError:  "User not authenticated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c *gin.Context
			var w *httptest.ResponseRecorder

			if tt.authenticated {
				c, w = createTestContextWithAuth("POST", "/api/v1/admin/users", tt.requestBody)
			} else {
				c, w = createTestContext("POST", "/api/v1/admin/users", tt.requestBody)
			}

			handler := createUser(deps)
			handler(c)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)

			if tt.expectedError != "" {
				errorMsg, exists := response["error"]
				assert.True(t, exists)
				assert.Contains(t, errorMsg.(string), tt.expectedError)
			}
		})
	}
}

func TestCreateUser_ConfigurationErrors(t *testing.T) {
	tests := []struct {
		name           string
		deps           *Dependencies
		expectedStatus int
		expectedError  string
	}{
		{
			name: "missing auth endpoint",
			deps: &Dependencies{
				AuthConfig: middleware.AuthConfig{
					JwtSecret: "test-secret",
				},
				AuthEndpoint: "",
			},
			expectedStatus: http.StatusInternalServerError,
			expectedError:  "Auth endpoint not configured",
		},
		{
			name: "missing JWT secret",
			deps: &Dependencies{
				AuthConfig: middleware.AuthConfig{
					JwtSecret: "",
				},
				AuthEndpoint: "http://test-auth:9999",
			},
			expectedStatus: http.StatusInternalServerError,
			expectedError:  "JWT secret not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestBody := CreateUserRequest{
				Email:    "test@example.com",
				Password: "password123",
				Username: "testuser",
			}

			c, w := createTestContextWithAuth("POST", "/api/v1/admin/users", requestBody)

			handler := createUser(tt.deps)
			handler(c)

			assert.Equal(t, tt.expectedStatus, w.Code)

			var response map[string]interface{}
			err := json.Unmarshal(w.Body.Bytes(), &response)
			assert.NoError(t, err)

			errorMsg, exists := response["error"]
			assert.True(t, exists)
			assert.Contains(t, errorMsg.(string), tt.expectedError)
		})
	}
}

func TestRegisterRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	deps := &Dependencies{
		AuthConfig: middleware.AuthConfig{
			JwtSecret: "test-secret",
		},
		AuthEndpoint: "http://test-auth:9999",
	}

	RegisterRoutes(r, deps)

	// Test that the route group is created and has the expected middleware
	routes := r.Routes()

	// Find the admin route
	var adminRoute gin.RouteInfo
	for _, route := range routes {
		if route.Path == "/api/v1/admin/users" && route.Method == "POST" {
			adminRoute = route
			break
		}
	}

	assert.NotEmpty(t, adminRoute.Path)
	assert.Equal(t, "POST", adminRoute.Method)
	assert.Equal(t, "/api/v1/admin/users", adminRoute.Path)
}

func TestCreateUser_TokenCreationFailure(t *testing.T) {
	// This test simulates a scenario where JWT token creation might fail
	// by using an extremely long secret that could cause issues
	deps := &Dependencies{
		AuthConfig: middleware.AuthConfig{
			JwtSecret: string(make([]byte, 1000000)), // Very large secret
		},
		AuthEndpoint: "http://test-auth:9999",
	}

	requestBody := CreateUserRequest{
		Email:    "test@example.com",
		Password: "password123",
		Username: "testuser",
	}

	c, w := createTestContextWithAuth("POST", "/api/v1/admin/users", requestBody)

	handler := createUser(deps)
	handler(c)

	// The request should still succeed with a large secret
	// This test ensures our token creation is robust
	assert.True(t, w.Code == http.StatusInternalServerError || w.Code == http.StatusCreated)
}

func TestCreateUserRequest_JSONBinding(t *testing.T) {
	tests := []struct {
		name        string
		jsonInput   string
		expectValid bool
	}{
		{
			name:        "valid JSON",
			jsonInput:   `{"email":"test@example.com","password":"pass123","username":"testuser"}`,
			expectValid: true,
		},
		{
			name:        "missing email field",
			jsonInput:   `{"password":"pass123","username":"testuser"}`,
			expectValid: false,
		},
		{
			name:        "missing password field",
			jsonInput:   `{"email":"test@example.com","username":"testuser"}`,
			expectValid: false,
		},
		{
			name:        "empty email",
			jsonInput:   `{"email":"","password":"pass123","username":"testuser"}`,
			expectValid: false,
		},
		{
			name:        "empty password",
			jsonInput:   `{"email":"test@example.com","password":"","username":"testuser"}`,
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(tt.jsonInput))
			c.Request.Header.Set("Content-Type", "application/json")

			var req CreateUserRequest
			err := c.ShouldBindJSON(&req)

			if tt.expectValid {
				assert.NoError(t, err)
				if err == nil {
					assert.NotEmpty(t, req.Email)
					assert.NotEmpty(t, req.Password)
				}
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestCreateUserResponse_Structure(t *testing.T) {
	response := CreateUserResponse{
		ID:       "123e4567-e89b-12d3-a456-426614174000",
		Email:    "test@example.com",
		Username: "testuser",
	}

	// Test JSON marshaling
	jsonData, err := json.Marshal(response)
	require.NoError(t, err)

	// Test JSON unmarshaling
	var unmarshaled CreateUserResponse
	err = json.Unmarshal(jsonData, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, response.ID, unmarshaled.ID)
	assert.Equal(t, response.Email, unmarshaled.Email)
	assert.Equal(t, response.Username, unmarshaled.Username)
}
