package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAuthHeaderValue(t *testing.T) {
	tests := []struct {
		name string
		auth *ExternalEndpointAuthSpec
		want string
	}{
		{
			name: "bearer type",
			auth: &ExternalEndpointAuthSpec{Type: ExternalEndpointAuthTypeBearer, Credential: "my-token"},
			want: "Bearer my-token",
		},
		{
			name: "api_key type",
			auth: &ExternalEndpointAuthSpec{Type: ExternalEndpointAuthTypeAPIKey, Credential: "sk-abc123"},
			want: "sk-abc123",
		},
		{
			name: "unknown type returns credential directly",
			auth: &ExternalEndpointAuthSpec{Type: "custom", Credential: "raw-value"},
			want: "raw-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.auth.AuthHeaderValue())
		})
	}
}

func TestExternalEndpoint_Key(t *testing.T) {
	tests := []struct {
		name string
		ee   ExternalEndpoint
		want string
	}{
		{
			name: "nil metadata",
			ee:   ExternalEndpoint{ID: 42},
			want: "default-external-endpoint-42",
		},
		{
			name: "empty workspace",
			ee: ExternalEndpoint{
				ID:       7,
				Metadata: &Metadata{Name: "my-ext"},
			},
			want: "default-external-endpoint-7-my-ext",
		},
		{
			name: "with workspace",
			ee: ExternalEndpoint{
				ID:       3,
				Metadata: &Metadata{Name: "gpt", Workspace: "prod"},
			},
			want: "prod-external-endpoint-3-gpt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.ee.Key())
		})
	}
}
