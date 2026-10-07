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
	// LDAP is nil when no LDAP directory is configured; the LDAP login route is
	// then not registered.
	LDAP LDAPAuthenticator
	// OIDC is nil when no OpenID Connect provider is configured; the OIDC login
	// routes are then not registered.
	OIDC *OIDCLogin
	// Sessions is required when LDAP or OIDC is set.
	Sessions auth.SessionIssuer
}

// RegisterAuthRoutes registers authentication-related routes
func RegisterAuthRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	authMiddleware := middleware.Auth(middleware.Dependencies{
		Config:  deps.AuthConfig,
		Storage: deps.Storage,
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
	// Only expose endpoints actually used by the client. There is no /signup:
	// users are created by an admin or by an SSO login, never by themselves.
	authGroup.POST("/token", handleTokenProxy(deps))   // signInWithPassword, token refresh
	authGroup.POST("/recover", handleAuthProxy(deps))  // resetPasswordForEmail
	authGroup.GET("/user", handleAuthProxy(deps))      // getUser
	authGroup.PUT("/user", handleUpdateUser(deps))     // updateUser (password)
	authGroup.POST("/logout", handleAuthProxy(deps))   // signOut
	authGroup.POST("/verify", handleVerifyProxy(deps)) // verifyOtp, finishes an OIDC login

	if deps.LDAP != nil {
		authGroup.POST("/ldap/token", handleLDAPToken(deps))
	}

	if deps.OIDC != nil {
		authGroup.GET("/oidc/authorize", handleOIDCAuthorize(deps.OIDC))
		authGroup.GET("/oidc/callback", handleOIDCCallback(deps))
	}
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

// handleVerifyProxy proxies /verify for magic link tokens only, which is how an
// OIDC login ends. GoTrue's other token types belong to flows neutree does not
// expose through this route.
func handleVerifyProxy(deps *Dependencies) gin.HandlerFunc {
	proxyHandler := proxies.CreateProxyHandler(deps.AuthEndpoint, "verify", nil)

	return func(c *gin.Context) {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		c.Request.Body.Close()

		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}

		var body struct {
			Type string `json:"type"`
		}

		if err := json.Unmarshal(bodyBytes, &body); err != nil || body.Type != "magiclink" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "only magiclink verification is supported"})
			return
		}

		request.RestoreBody(c, bodyBytes)

		proxyHandler(c)
	}
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
