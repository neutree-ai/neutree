package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"

	"github.com/neutree-ai/neutree/internal/middleware"
)

type Dependencies struct {
	AuthConfig   middleware.AuthConfig
	AuthEndpoint string
}

type CreateUserRequest struct {
	Email    string `json:"email" binding:"required"`
	Password string `json:"password" binding:"required"`
	Username string `json:"username"`
}

type CreateUserResponse struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

// RegisterRoutes registers admin-related routes
func RegisterRoutes(r *gin.Engine, deps *Dependencies) {
	adminGroup := r.Group("/api/v1/admin")
	// Apply authentication middleware for admin-only access
	adminGroup.Use(middleware.Auth(middleware.Dependencies{
		Config: deps.AuthConfig,
	}))

	adminGroup.POST("/users", createUser(deps))
}

func createUser(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var reqData CreateUserRequest
		if err := c.ShouldBindJSON(&reqData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Check if current user has admin permissions
		_, exists := middleware.GetUserID(c)
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
			return
		}

		gotureUser := map[string]interface{}{
			"email":         reqData.Email,
			"password":      reqData.Password,
			"email_confirm": true,
		}

		if reqData.Username != "" {
			gotureUser["user_metadata"] = map[string]string{
				"username": reqData.Username,
			}
		}

		jsonData, err := json.Marshal(gotureUser)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare request"})
			return
		}

		// Call GoTrue admin endpoint with service role token
		adminURL := fmt.Sprintf("%s/admin/users", deps.AuthEndpoint)

		// Create service role JWT token for GoTrue admin API
		serviceToken, err := createServiceRoleToken(deps.AuthConfig.JwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create service token"})
			return
		}

		httpReq, err := http.NewRequest("POST", adminURL, bytes.NewBuffer(jsonData))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create request"})
			return
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+serviceToken)

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(httpReq)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
			return
		}

		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			c.JSON(resp.StatusCode, gin.H{"error": "Failed to create user in auth service"})
			return
		}

		var createdUser CreateUserResponse
		if err := json.NewDecoder(resp.Body).Decode(&createdUser); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to parse response"})
			return
		}

		c.JSON(http.StatusCreated, createdUser)
	}
}

// createServiceRoleToken creates a JWT token with service_role claim for GoTrue admin API
func createServiceRoleToken(jwtSecret string) (string, error) {
	token := jwt.New(jwt.SigningMethodHS256)
	claims, ok := token.Claims.(jwt.MapClaims)

	if !ok {
		return "", fmt.Errorf("failed to create claims")
	}

	claims["role"] = "service_role"
	claims["iss"] = "neutree"
	claims["iat"] = time.Now().Unix()
	claims["exp"] = time.Now().Add(time.Hour).Unix()

	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}
