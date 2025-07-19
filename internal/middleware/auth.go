package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Config  AuthConfig
	Storage storage.Storage
}

// AuthConfig holds the configuration for JWT authentication
type AuthConfig struct {
	JwtSecret string
}

// Claims represents the JWT claims structure
type Claims struct {
	UserID string `json:"sub"`
	Email  string `json:"email,omitempty"`
	jwt.RegisteredClaims
}

type ParsedInfo struct {
	UserID string `json:"user_id"`
}

func Auth(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract token from Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authorization header is required",
			})
			c.Abort()

			return
		}

		var parsedInfo *ParsedInfo
		var err error

		switch {
		case strings.HasPrefix(authHeader, "Bearer "):
			parsedInfo, err = parseBearerToken(deps.Config, authHeader)
		case strings.HasPrefix(authHeader, "sk_"):
			parsedInfo, err = parseApiKey(deps.Storage, authHeader)
		default:
			err = errors.New("invalid Authorization header format")
		}

		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": err.Error(),
			})
			c.Abort()

			return
		}

		// Set user information in context
		c.Set("user_id", parsedInfo.UserID)

		klog.V(4).Infof("Authenticated user: %s", parsedInfo.UserID)

		c.Next()
	}
}

func parseBearerToken(config AuthConfig, authHeader string) (*ParsedInfo, error) {
	// Extract the token
	tokenString := strings.TrimPrefix(authHeader, "Bearer ")
	if tokenString == "" {
		return nil, errors.New("token is required")
	}

	// Parse and validate the token
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate the signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}

		return []byte(config.JwtSecret), nil
	})

	if err != nil || !token.Valid {
		klog.V(4).Infof("JWT validation failed: %v", err)
		return nil, errors.New("invalid token")
	}

	// Extract claims
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, errors.New("invalid token claims")
	}

	return &ParsedInfo{
		UserID: claims.UserID,
	}, nil
}

func parseApiKey(s storage.Storage, authHeader string) (*ParsedInfo, error) {
	apiKey, err := s.ListApiKey(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "status->sk_value",
				Operator: "eq",
				Value:    strconv.Quote(authHeader),
			},
		},
	})

	if err != nil {
		return nil, errors.Join(err, errors.New("failed to list API keys"))
	}

	if len(apiKey) == 0 {
		return nil, errors.New("API key not found")
	}

	return &ParsedInfo{
		UserID: apiKey[0].UserID,
	}, nil
}

// GetUserID extracts user ID from Gin context
func GetUserID(c *gin.Context) (string, bool) {
	userID, exists := c.Get("user_id")
	if !exists {
		return "", false
	}

	userIDStr, ok := userID.(string)

	return userIDStr, ok
}
