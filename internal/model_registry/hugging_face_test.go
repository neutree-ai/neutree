package model_registry

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

type MockRoundTripper struct {
	RoundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.RoundTripFunc(req)
}

func TestNewHuggingFace(t *testing.T) {
	tests := []struct {
		name          string
		registry      *v1.ModelRegistry
		wantErr       bool
		wantErrString string
	}{
		{
			name: "registry with empty url",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "",
				},
			},
			wantErr:       true,
			wantErrString: "cannot be empty",
		},
		{
			name: "registry with invalid url, no scheme",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "invalid-url",
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "registry with valid url, no host",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "http://",
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "registry with valid url, unsupport character",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url: `
					`,
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "normal registry",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "https://huggingface.co",
				},
			},
			wantErr:       false,
			wantErrString: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newHuggingFace(tt.registry)
			if tt.wantErr {
				assert.ErrorContains(t, err, tt.wantErrString)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHuggingFace_HealthyCheck(t *testing.T) {
	tests := []struct {
		name          string
		apiToken      string
		mockResponse  func(req *http.Request) (*http.Response, error)
		wantErr       bool
		wantErrString string
	}{
		{
			name:     "success without token",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`[{"modelId": "test-model"}]`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr: false,
		},
		{
			name:     "success with valid token",
			apiToken: "valid-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					assert.Equal(t, "Bearer valid-token", req.Header.Get("Authorization"))
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`{"name": "test-user"}`)),
						Header:     make(http.Header),
					}, nil
				}
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`[{"modelId": "test-model"}]`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr: false,
		},
		{
			name:     "invalid token - whoami fails",
			apiToken: "invalid-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body:       io.NopCloser(bytes.NewBufferString(`{"error": "Invalid token"}`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "invalid Hugging Face API token",
		},
		{
			name:     "list models fails",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Body:       io.NopCloser(bytes.NewBufferString(`{"error": "Internal server error"}`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "failed to list models from Hugging Face API",
		},
		{
			name:     "network error on whoami",
			apiToken: "test-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					return nil, errors.New("network timeout")
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "invalid Hugging Face API token",
		},
		{
			name:     "network error on list models",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return nil, errors.New("connection refused")
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "failed to list models from Hugging Face API",
		},
		{
			name:     "invalid json response from whoami",
			apiToken: "test-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`invalid json`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "invalid Hugging Face API token",
		},
		{
			name:     "invalid json response from list models",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`not a valid json`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "failed to list models from Hugging Face API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock HTTP client
			mockClient := &http.Client{
				Transport: &MockRoundTripper{
					RoundTripFunc: tt.mockResponse,
				},
			}

			hf := &huggingFace{
				url:      "https://huggingface.co",
				apiToken: tt.apiToken,
				client:   mockClient,
			}

			err := hf.healthyCheck()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrString)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
