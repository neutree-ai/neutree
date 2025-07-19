package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestAuth(t *testing.T) {
	// Set Gin to test mode
	gin.SetMode(gin.TestMode)

	// Test JWT secret
	jwtSecret := "test-secret"
	config := AuthConfig{
		JwtSecret: jwtSecret,
	}

	tests := []struct {
		name           string
		setupAuth      func() string
		setupMock      func(*storagemocks.MockStorage)
		expectedStatus int
		expectUserID   string
	}{
		{
			name: "Valid JWT token",
			setupAuth: func() string {
				claims := &Claims{
					UserID: "user-123",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
						IssuedAt:  jwt.NewNumericDate(time.Now()),
					},
				}

				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				// No mock setup needed for JWT
			},
			expectedStatus: http.StatusOK,
			expectUserID:   "user-123",
		},
		{
			name: "Valid API Key",
			setupAuth: func() string {
				return "sk_test_api_key_123"
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				apiKeys := []v1.ApiKey{
					{
						UserID: "user-456",
					},
				}
				mockStorage.On("ListApiKey", mock.MatchedBy(func(opt storage.ListOption) bool {
					return len(opt.Filters) == 1 &&
						opt.Filters[0].Column == "status->sk_value" &&
						opt.Filters[0].Operator == "eq" &&
						opt.Filters[0].Value == `"sk_test_api_key_123"`
				})).Return(apiKeys, nil)
			},
			expectedStatus: http.StatusOK,
			expectUserID:   "user-456",
		},
		{
			name: "API Key not found",
			setupAuth: func() string {
				return "sk_invalid_key"
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				mockStorage.On("ListApiKey", mock.Anything).Return([]v1.ApiKey{}, nil)
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "API Key storage error",
			setupAuth: func() string {
				return "sk_test_api_key"
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				mockStorage.On("ListApiKey", mock.Anything).Return(nil, errors.New("database error"))
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Missing Authorization header",
			setupAuth: func() string {
				return ""
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				// No mock setup needed
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Invalid Authorization header format",
			setupAuth: func() string {
				return "Invalid header"
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				// No mock setup needed
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Empty Bearer token",
			setupAuth: func() string {
				return "Bearer "
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				// No mock setup needed
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Invalid JWT token",
			setupAuth: func() string {
				return "Bearer invalid-token"
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				// No mock setup needed
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Expired JWT token",
			setupAuth: func() string {
				claims := &Claims{
					UserID: "user-123",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour)), // Expired
						IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
					},
				}

				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				// No mock setup needed
			},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock storage
			mockStorage := &storagemocks.MockStorage{}
			tt.setupMock(mockStorage)

			// Create test router
			r := gin.New()
			r.Use(Auth(Dependencies{
				Config:  config,
				Storage: mockStorage,
			}))
			r.GET("/test", func(c *gin.Context) {
				userID, _ := GetUserID(c)
				c.JSON(http.StatusOK, gin.H{
					"user_id": userID,
				})
			})

			// Create test request
			req := httptest.NewRequest("GET", "/test", nil)

			// Set up authorization header
			authHeader := tt.setupAuth()
			if authHeader != "" {
				req.Header.Set("Authorization", authHeader)
			}

			// Create response recorder
			w := httptest.NewRecorder()

			// Perform request
			r.ServeHTTP(w, req)

			// Check status code
			assert.Equal(t, tt.expectedStatus, w.Code)

			// Check user ID if successful
			if tt.expectedStatus == http.StatusOK && tt.expectUserID != "" {
				var response map[string]interface{}
				err := json.Unmarshal(w.Body.Bytes(), &response)
				assert.NoError(t, err)
				assert.Equal(t, tt.expectUserID, response["user_id"])
			}

			// Assert mock expectations
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestParseBearerToken(t *testing.T) {
	jwtSecret := "test-secret"
	config := AuthConfig{
		JwtSecret: jwtSecret,
	}

	tests := []struct {
		name        string
		authHeader  string
		setupToken  func() string
		expectError bool
		expectID    string
	}{
		{
			name: "Valid bearer token",
			setupToken: func() string {
				claims := &Claims{
					UserID: "user-123",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
						IssuedAt:  jwt.NewNumericDate(time.Now()),
					},
				}
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			expectError: false,
			expectID:    "user-123",
		},
		{
			name:        "Empty token",
			authHeader:  "Bearer ",
			expectError: true,
		},
		{
			name:        "Invalid token",
			authHeader:  "Bearer invalid-token",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authHeader := tt.authHeader
			if tt.setupToken != nil {
				authHeader = tt.setupToken()
			}

			parsedInfo, err := parseBearerToken(config, authHeader)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, parsedInfo)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, parsedInfo)
				assert.Equal(t, tt.expectID, parsedInfo.UserID)
			}
		})
	}
}

func TestParseApiKey(t *testing.T) {
	tests := []struct {
		name        string
		apiKey      string
		setupMock   func(*storagemocks.MockStorage)
		expectError bool
		expectID    string
	}{
		{
			name:   "Valid API key",
			apiKey: "sk_test_key",
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				apiKeys := []v1.ApiKey{
					{
						UserID: "user-456",
					},
				}
				mockStorage.On("ListApiKey", mock.Anything).Return(apiKeys, nil)
			},
			expectError: false,
			expectID:    "user-456",
		},
		{
			name:   "API key not found",
			apiKey: "sk_invalid_key",
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				mockStorage.On("ListApiKey", mock.Anything).Return([]v1.ApiKey{}, nil)
			},
			expectError: true,
		},
		{
			name:   "Storage error",
			apiKey: "sk_test_key",
			setupMock: func(mockStorage *storagemocks.MockStorage) {
				mockStorage.On("ListApiKey", mock.Anything).Return(nil, errors.New("database error"))
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.setupMock(mockStorage)

			parsedInfo, err := parseApiKey(mockStorage, tt.apiKey)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, parsedInfo)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, parsedInfo)
				assert.Equal(t, tt.expectID, parsedInfo.UserID)
			}

			mockStorage.AssertExpectations(t)
		})
	}
}

func TestGetUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		setupContext func(*gin.Context)
		expectedID   string
		expectedOK   bool
	}{
		{
			name: "Valid user ID in context",
			setupContext: func(c *gin.Context) {
				c.Set("user_id", "user-123")
			},
			expectedID: "user-123",
			expectedOK: true,
		},
		{
			name: "No user ID in context",
			setupContext: func(c *gin.Context) {
				// Don't set anything
			},
			expectedID: "",
			expectedOK: false,
		},
		{
			name: "Invalid type in context",
			setupContext: func(c *gin.Context) {
				c.Set("user_id", 123) // Wrong type
			},
			expectedID: "",
			expectedOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			tt.setupContext(c)

			userID, ok := GetUserID(c)
			assert.Equal(t, tt.expectedID, userID)
			assert.Equal(t, tt.expectedOK, ok)
		})
	}
}
