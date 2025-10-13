package dashboard

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestEndpointToServeApplicationName(t *testing.T) {
	tests := []struct {
		name     string
		endpoint *v1.Endpoint
		expected string
	}{
		{
			name: "basic endpoint with workspace and name",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "production",
					Name:      "chat-model",
				},
			},
			expected: "production_chat-model",
		},
		{
			name: "default workspace",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "test-endpoint",
				},
			},
			expected: "default_test-endpoint",
		},
		{
			name: "workspace with special characters",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "dev-test",
					Name:      "model-v1",
				},
			},
			expected: "dev-test_model-v1",
		},
		{
			name: "empty workspace",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "",
					Name:      "endpoint",
				},
			},
			expected: "_endpoint",
		},
		{
			name: "empty name",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{
					Workspace: "workspace",
					Name:      "",
				},
			},
			expected: "workspace_",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EndpointToServeApplicationName(tt.endpoint)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEndpointToApplication_ApplicationNameConsistency(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "0.5.0",
			},
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]float64{},
			},
			DeploymentOptions: map[string]interface{}{},
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
		},
	}

	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
		},
	}

	app := EndpointToApplication(endpoint, modelRegistry)

	// Verify that the application name matches the naming function
	expectedName := EndpointToServeApplicationName(endpoint)
	assert.Equal(t, expectedName, app.Name)
	assert.Equal(t, "production_chat-model", app.Name)
}

func TestEndpointToApplication_RouteConsistency(t *testing.T) {
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  "vllm",
				Version: "0.5.0",
			},
			Resources: &v1.ResourceSpec{
				Accelerator: map[string]float64{},
			},
			DeploymentOptions: map[string]interface{}{},
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
		},
	}

	modelRegistry := &v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
		},
	}

	app := EndpointToApplication(endpoint, modelRegistry)

	// Verify that the route prefix includes workspace
	assert.Equal(t, "/production/chat-model", app.RoutePrefix)
}

func TestFormatServiceURL_WorkspaceInURL(t *testing.T) {
	cluster := &v1.Cluster{
		Status: &v1.ClusterStatus{
			DashboardURL: "http://ray-dashboard.example.com:8265",
		},
	}

	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Workspace: "production",
			Name:      "chat-model",
		},
	}

	url, err := FormatServiceURL(cluster, endpoint)
	assert.NoError(t, err)
	assert.Equal(t, "http://ray-dashboard.example.com:8000/production/chat-model", url)
}
