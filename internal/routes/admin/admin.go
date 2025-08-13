package admin

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/supabase-community/gotrue-go"
	"github.com/supabase-community/gotrue-go/types"

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

		if reqData.Username == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Username is required"})
			return
		}

		// Ensure current user is authenticated (admin check can be added here)
		if _, exists := middleware.GetUserID(c); !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "User not authenticated"})
			return
		}

		// Validate AuthEndpoint is configured
		if deps.AuthEndpoint == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Auth endpoint not configured"})
			return
		}

		// Validate JWT secret is configured
		if deps.AuthConfig.JwtSecret == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "JWT secret not configured"})
			return
		}

		// Create GoTrue client and set service role token
		tokenStr, err := createServiceRoleToken(deps.AuthConfig.JwtSecret)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create service token."})
			return
		}

		client := gotrue.New("", "").WithCustomGoTrueURL(deps.AuthEndpoint).WithToken(tokenStr)
		userParams := types.AdminCreateUserRequest{
			Email:        reqData.Email,
			Password:     &reqData.Password,
			EmailConfirm: true,
			UserMetadata: map[string]any{
				"username": reqData.Username,
			},
		}

		user, err := client.AdminCreateUser(userParams)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		resp := CreateUserResponse{
			ID:       user.ID.String(),
			Email:    user.Email,
			Username: "",
		}

		if val, ok := user.UserMetadata["username"].(string); ok {
			resp.Username = val
		}

		c.JSON(http.StatusCreated, resp)
	}
}

// createServiceRoleToken creates a JWT token with service_role claim for GoTrue admin API
func createServiceRoleToken(jwtSecret string) (string, error) {
	const (
		JWTServiceRoleTokenExpiry = time.Hour
	)
	token := jwt.New(jwt.SigningMethodHS256)
	claims, ok := token.Claims.(jwt.MapClaims)

	if !ok {
		return "", fmt.Errorf("failed to create claims")
	}

	claims["role"] = "service_role"
	claims["iss"] = "neutree"
	claims["iat"] = time.Now().Unix()
	claims["exp"] = time.Now().Add(JWTServiceRoleTokenExpiry).Unix()

	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", err
	}

	return tokenString, nil
}
