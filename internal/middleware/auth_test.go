package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
)

func TestJWTAuth(t *testing.T) {
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
		expectedStatus int
		expectUserID   string
	}{
		{
			name: "Valid JWT token",
			setupAuth: func() string {
				claims := &Claims{
					UserID: "user-123",
					Email:  "test@example.com",
					Role:   "user",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
						IssuedAt:  jwt.NewNumericDate(time.Now()),
					},
				}

				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			expectedStatus: http.StatusOK,
			expectUserID:   "user-123",
		},
		{
			name: "Missing Authorization header",
			setupAuth: func() string {
				return ""
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Invalid Authorization header format",
			setupAuth: func() string {
				return "Invalid header"
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Empty token",
			setupAuth: func() string {
				return "Bearer "
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Invalid token",
			setupAuth: func() string {
				return "Bearer invalid-token"
			},
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Expired token",
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
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name: "Token with wrong signing method",
			setupAuth: func() string {
				claims := &Claims{
					UserID: "user-123",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
						IssuedAt:  jwt.NewNumericDate(time.Now()),
					},
				}

				// Use wrong signing method
				token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
				tokenString, _ := token.SignedString([]byte(jwtSecret))
				return "Bearer " + tokenString
			},
			expectedStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test router
			r := gin.New()
			r.Use(JWTAuth(config))
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

func TestGetUserEmail(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("user_email", "test@example.com")

	email, ok := GetUserEmail(c)
	assert.True(t, ok)
	assert.Equal(t, "test@example.com", email)

	// Test missing email
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	email2, ok2 := GetUserEmail(c2)
	assert.False(t, ok2)
	assert.Equal(t, "", email2)
}
