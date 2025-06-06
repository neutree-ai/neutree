package system

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"
)

// Dependencies defines the dependencies for system handlers
type Dependencies struct {
	// GrafanaURL is the URL to the Grafana instance for monitoring
	GrafanaURL string
}

// SystemInfo represents the system information response
type SystemInfo struct {
	// GrafanaURL is the URL to access Grafana dashboard
	GrafanaURL string `json:"grafana_url,omitempty"`
}

// RegisterRoutes registers system-related routes
func RegisterRoutes(r *gin.Engine, deps *Dependencies) {
	apiV1 := r.Group("/api/v1")

	// System information endpoint
	apiV1.GET("/system/info", handleSystemInfo(deps))
}

// handleSystemInfo returns system information including URLs to monitoring services
func handleSystemInfo(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		info := &SystemInfo{}

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
	// Parse the URL to validate it
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	// Ensure the URL has a scheme
	if parsedURL.Scheme == "" {
		parsedURL.Scheme = "http"
	}

	// Clean up the URL (remove trailing slashes, etc.)
	cleanURL := parsedURL.String()
	cleanURL = strings.TrimSuffix(cleanURL, "/")

	return cleanURL, nil
}
