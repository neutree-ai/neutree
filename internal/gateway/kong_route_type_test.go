package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// NEU-633: an endpoint whose engine ships its own model (e.g. Flex) may have
// a nil Spec.Model. getEndpointRouteType must fall back to the default route
// type instead of dereferencing it.
func TestGetEndpointRouteType_NilModel(t *testing.T) {
	ep := &v1.Endpoint{
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{Engine: "flex", Version: "1.0.0"},
		},
	}

	assert.Equal(t, v1.RouteTypeChatCompletions, getEndpointRouteType(ep))
}

func TestGetEndpointRouteType_ByTask(t *testing.T) {
	tests := []struct {
		task     string
		expected string
	}{
		{v1.TextGenerationModelTask, v1.RouteTypeChatCompletions},
		{v1.TextEmbeddingModelTask, v1.RouteTypeEmbeddings},
		{v1.TextRerankModelTask, v1.RouteTypeRerank},
		{"", v1.RouteTypeChatCompletions},
	}

	for _, tt := range tests {
		ep := &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Model: &v1.ModelSpec{Task: tt.task},
			},
		}

		assert.Equal(t, tt.expected, getEndpointRouteType(ep))
	}
}
