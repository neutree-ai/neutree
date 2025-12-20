package client

import (
	"crypto/tls"
	"net/http"
	"time"
)

// Client represents a neutree API client
type Client struct {
	// Common client properties
	baseURL    string
	apiKey     string
	httpClient *http.Client

	// Service endpoints
	Models          *ModelsService
	Engines         *EnginesService
	ImageRegistries *ImageRegistriesService
	ModelRegistries *ModelRegistriesService
	// Other services will be added here
}

// ClientOption is a function that configures a Client
type ClientOption func(*Client)

// WithAPIKey sets the API key for the client
func WithAPIKey(apiKey string) ClientOption {
	return func(c *Client) {
		c.apiKey = apiKey
	}
}

// WithHTTPClient sets the HTTP client for the API client
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

func WithInsecureSkipVerify() ClientOption {
	return func(c *Client) {
		if c.httpClient == nil || c.httpClient.Transport == nil {
			transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck
			transport.TLSClientConfig = &tls.Config{
				//nolint:gosec
				InsecureSkipVerify: true,
			}

			if c.httpClient == nil {
				c.httpClient = &http.Client{
					Timeout: 30 * time.Second,
				}
			}

			if c.httpClient.Transport == nil {
				c.httpClient.Transport = transport
			}
		} else {
			if transport, ok := c.httpClient.Transport.(*http.Transport); ok {
				if transport.TLSClientConfig == nil {
					transport.TLSClientConfig = &tls.Config{} //nolint:gosec
				}
				//nolint:gosec
				transport.TLSClientConfig.InsecureSkipVerify = true
				c.httpClient.Transport = transport
			}
		}
	}
}

// WithTimeout sets the timeout for the default HTTP client
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{
				Timeout: timeout,
			}
		} else {
			c.httpClient.Timeout = timeout
		}
	}
}

// NewClient creates a new neutree API client
func NewClient(baseURL string, options ...ClientOption) *Client {
	client := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	// Apply options
	for _, option := range options {
		option(client)
	}

	// Initialize services
	client.Models = NewModelsService(client)
	client.Engines = NewEnginesService(client)
	client.ImageRegistries = NewImageRegistriesService(client)
	client.ModelRegistries = NewModelRegistriesService(client)
	// Other services will be initialized here

	return client
}

// do performs an HTTP request using the client's HTTP client
func (c *Client) do(req *http.Request) (*http.Response, error) {
	// Add authorization header if API key is set
	if c.apiKey != "" {
		req.Header.Set("Authorization", c.apiKey)
	}

	return c.httpClient.Do(req)
}
