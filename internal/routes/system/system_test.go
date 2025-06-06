package system

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// createTestContext creates a test context for testing
func createTestContext(method, path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	req := httptest.NewRequest(method, path, nil)
	c.Request = req

	return c, w
}

// TestHandleSystemInfo_WithGrafanaURL tests system info with Grafana URL configured
func TestHandleSystemInfo_WithGrafanaURL(t *testing.T) {
	deps := &Dependencies{
		GrafanaURL: "http://example.com:3030",
	}

	c, w := createTestContext("GET", "/api/v1/system/info")

	handler := handleSystemInfo(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response SystemInfo
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Equal(t, "http://example.com:3030", response.GrafanaURL)
}

// TestHandleSystemInfo_WithoutGrafanaURL tests system info without Grafana URL configured
func TestHandleSystemInfo_WithoutGrafanaURL(t *testing.T) {
	deps := &Dependencies{
		GrafanaURL: "",
	}

	c, w := createTestContext("GET", "/api/v1/system/info")

	handler := handleSystemInfo(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response SystemInfo
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Empty(t, response.GrafanaURL)
}

// TestHandleSystemInfo_WithTrailingSlash tests URL cleaning
func TestHandleSystemInfo_WithTrailingSlash(t *testing.T) {
	deps := &Dependencies{
		GrafanaURL: "http://example.com:3030/",
	}

	c, w := createTestContext("GET", "/api/v1/system/info")

	handler := handleSystemInfo(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response SystemInfo
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	// Should remove trailing slash
	assert.Equal(t, "http://example.com:3030", response.GrafanaURL)
}

// TestHandleSystemInfo_WithInvalidURL tests handling of invalid URLs
func TestHandleSystemInfo_WithInvalidURL(t *testing.T) {
	deps := &Dependencies{
		GrafanaURL: "://invalid-url",
	}

	c, w := createTestContext("GET", "/api/v1/system/info")

	handler := handleSystemInfo(deps)
	handler(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var response SystemInfo
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	// Invalid URL should be omitted
	assert.Empty(t, response.GrafanaURL)
}

// TestValidateAndCleanURL tests the URL validation and cleaning function
func TestValidateAndCleanURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		hasError bool
	}{
		{
			name:     "Valid HTTP URL",
			input:    "http://example.com:3030",
			expected: "http://example.com:3030",
			hasError: false,
		},
		{
			name:     "Valid HTTPS URL",
			input:    "https://example.com:3030",
			expected: "https://example.com:3030",
			hasError: false,
		},
		{
			name:     "URL with trailing slash",
			input:    "http://example.com:3030/",
			expected: "http://example.com:3030",
			hasError: false,
		},
		{
			name:     "URL without scheme",
			input:    "example.com:3030",
			expected: "http://example.com:3030",
			hasError: false,
		},
		{
			name:     "Invalid URL",
			input:    "://invalid",
			expected: "",
			hasError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := validateAndCleanURL(tt.input)

			if tt.hasError {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
