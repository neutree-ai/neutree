package system

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/middleware"
)

// Dependencies defines the dependencies for system handlers
type Dependencies struct {
	// GrafanaURL is the URL to the Grafana instance for monitoring
	GrafanaURL string
	// Version is the application version
	Version string
	// AuthConfig is the JWT authentication configuration (required)
	AuthConfig middleware.AuthConfig
}

// SystemInfo represents the system information response
type SystemInfo struct {
	// GrafanaURL is the URL to access Grafana dashboard
	GrafanaURL string `json:"grafana_url,omitempty"`
	// Version is the application version
	Version string `json:"version,omitempty"`
}

// RegisterRoutes registers system-related routes
func RegisterRoutes(r *gin.Engine, deps *Dependencies) {
	apiV1 := r.Group("/api/v1")

	authMiddleware := middleware.Auth(middleware.Dependencies{
		Config: deps.AuthConfig,
	})
	apiV1.Use(authMiddleware)

	// System information endpoint
	apiV1.GET("/system/info", handleSystemInfo(deps))
}

// handleSystemInfo returns system information including URLs to monitoring services
func handleSystemInfo(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := &SystemInfo{}

		// Add version if configured
		if deps.Version != "" {
			info.Version = deps.Version
		}

		// Add Grafana URL if configured
		if deps.GrafanaURL != "" {
			// Validate and clean the URL
			if grafanaURL, err := validateAndCleanURL(deps.GrafanaURL); err == nil {
				info.GrafanaURL = grafanaURL
			} else {
				klog.Warningf("Invalid Grafana URL configured: %v", err)
			}
		}

		c.JSON(http.StatusOK, info)
	}
}

// validateAndCleanURL validates and cleans a URL string
func validateAndCleanURL(rawURL string) (string, error) {
	// Ensure the URL starts with http:// or https://
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "", fmt.Errorf("URL must start with http:// or https://: %s", rawURL)
	}

	// Parse the URL to validate it
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	// Clean up the URL (remove trailing slashes, etc.)
	cleanURL := parsedURL.String()
	cleanURL = strings.TrimSuffix(cleanURL, "/")

	return cleanURL, nil
}
