package util

import (
	"io"
	"net/http"
)

// HTTPClient interface abstracts HTTP operations for testing
type HTTPClient interface {
	Get(url string) (*http.Response, error)
}

// DefaultHTTPClient is the default implementation using http.DefaultClient
type DefaultHTTPClient struct{}

// Get performs an HTTP GET request
func (c *DefaultHTTPClient) Get(url string) (*http.Response, error) {
	return http.Get(url) // nolint:gosec // URL validation happens at call sites
}

// MockableHTTPResponse wraps http.Response for easier mocking
type MockableHTTPResponse struct {
	StatusCode int
	Body       io.ReadCloser
	Header     http.Header
}
