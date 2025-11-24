package auth

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/supabase-community/gotrue-go/types"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Dependencies defines the dependencies for auth handlers
type Dependencies struct {
	AuthEndpoint string
	AuthConfig   middleware.AuthConfig
	Storage      storage.Storage
	AuthClient   auth.Client
}

// RegisterAuthRoutes registers authentication-related routes
func RegisterAuthRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	authMiddleware := middleware.Auth(middleware.Dependencies{
		Config: deps.AuthConfig,
	})

	authGroup := group.Group("/auth")

	// Admin routes - require authentication
	adminGroup := authGroup.Group("/admin")
	adminGroup.Use(authMiddleware)
	{
		adminGroup.POST("/users", handleCreateUser(deps))
	}

	// Public GoTrue proxy routes - no authentication required
	// Only expose endpoints actually used by the client
	authGroup.POST("/token", handleAuthProxy(deps))    // signInWithPassword, token refresh
	authGroup.POST("/signup", handleAuthProxy(deps))   // signUp
	authGroup.POST("/recover", handleAuthProxy(deps))  // resetPasswordForEmail
	authGroup.GET("/user", handleAuthProxy(deps))      // getUser
	authGroup.PUT("/user", handleAuthProxy(deps))      // updateUser (password)
	authGroup.POST("/logout", handleAuthProxy(deps))   // signOut
	authGroup.GET("/authorize", handleAuthProxy(deps)) // OAuth authorize
	authGroup.GET("/callback", handleAuthProxy(deps))  // OAuth callback
}

func handleCreateUser(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var reqData CreateUserRequest

		if err := c.ShouldBindJSON(&reqData); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		resp, err := createUser(deps.AuthClient, reqData)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusCreated, resp)
	}
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

func createUser(client auth.Client, req CreateUserRequest) (*CreateUserResponse, error) {
	// Validate input
	if req.Username == "" {
		return nil, fmt.Errorf("username is required")
	}

	// Prepare user creation parameters
	userParams := types.AdminCreateUserRequest{
		Email:        req.Email,
		Password:     &req.Password,
		EmailConfirm: true,
		UserMetadata: map[string]any{
			"username": req.Username,
		},
	}

	// Call GoTrue API to create user
	user, err := client.AdminCreateUser(userParams)
	if err != nil {
		return nil, fmt.Errorf("failed to create user in GoTrue: %w", err)
	}

	// Build response
	resp := &CreateUserResponse{
		ID:       user.ID.String(),
		Email:    user.Email,
		Username: "",
	}

	if val, ok := user.UserMetadata["username"].(string); ok {
		resp.Username = val
	}

	return resp, nil
}

// handleAuthProxy proxies requests to the GoTrue backend
func handleAuthProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get the path relative to /auth
		path := c.Request.URL.Path[len("/api/v1/auth/"):]

		proxyHandler := proxies.CreateProxyHandler(deps.AuthEndpoint, path, nil)
		proxyHandler(c)
	}
}
