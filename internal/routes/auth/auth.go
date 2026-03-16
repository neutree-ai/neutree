package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/supabase-community/gotrue-go/types"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/internal/utils/request"
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
		adminGroup.POST("/users",
			middleware.RequirePermission("user_profile:create", middleware.PermissionDependencies{
				Storage: deps.Storage,
			}),
			handleCreateUser(deps))
	}

	// Public GoTrue proxy routes - no authentication required
	// Only expose endpoints actually used by the client
	authGroup.POST("/token", handleTokenProxy(deps))   // signInWithPassword, token refresh
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

// handleTokenProxy handles /token requests, resolving username to email before proxying to GoTrue
func handleTokenProxy(deps *Dependencies) gin.HandlerFunc {
	proxyHandler := proxies.CreateProxyHandler(deps.AuthEndpoint, "token", nil)

	return func(c *gin.Context) {
		grantType := c.Query("grant_type")
		if grantType != "password" {
			proxyHandler(c)
			return
		}

		bodyBytes, err := io.ReadAll(c.Request.Body)
		c.Request.Body.Close()

		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}

		bodyBytes = resolveEmailByUsername(deps.Storage, bodyBytes)

		request.RestoreBody(c, bodyBytes)

		proxyHandler(c)
	}
}

// resolveEmailByUsername tries to resolve the email field by looking up the username in user profiles.
// It is only called for password grant requests.
func resolveEmailByUsername(store storage.Storage, body []byte) []byte {
	var reqBody map[string]interface{}
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return body
	}

	identifier, _ := reqBody["email"].(string)
	if identifier == "" {
		return body
	}

	profiles, err := store.ListUserProfile(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(identifier),
			},
		},
	})
	if err != nil {
		klog.Warningf("Failed to resolve username %q to email: %v", identifier, err)
		return body
	}

	if len(profiles) == 0 {
		return body
	}

	if profiles[0].Spec != nil && profiles[0].Spec.Email != "" {
		reqBody["email"] = profiles[0].Spec.Email

		if modified, err := json.Marshal(reqBody); err == nil {
			return modified
		}
	}

	return body
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
