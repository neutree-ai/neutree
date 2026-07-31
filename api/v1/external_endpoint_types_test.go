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

func TestExternalEndpointUpstreamEntryIdentity(t *testing.T) {
	ref := "ep-a"

	tests := []struct {
		name       string
		entry      ExternalEndpointUpstreamEntry
		wantKind   string
		wantRef    string
		wantModels []string
	}{
		{
			name: "endpoint ref entry",
			entry: ExternalEndpointUpstreamEntry{
				EndpointRef:  &ref,
				ModelMapping: map[string]string{"b": "m2", "a": "m1"},
			},
			wantKind: ExternalEndpointUpstreamKindEndpointRef,
			wantRef:  "ep-a",
			// sorted so the derived status is stable across reconciles
			wantModels: []string{"a", "b"},
		},
		{
			name: "external upstream entry",
			entry: ExternalEndpointUpstreamEntry{
				Upstream: &ExternalEndpointUpstreamSpec{URL: "https://api.openai.com/v1"},
				Auth:     &ExternalEndpointAuthSpec{Type: ExternalEndpointAuthTypeBearer, Credential: "sk-secret"},
			},
			wantKind:   ExternalEndpointUpstreamKindExternal,
			wantRef:    "https://api.openai.com/v1",
			wantModels: nil,
		},
		{
			// userinfo in the URL is a credential; it must not reach the status
			name: "external upstream entry with embedded credentials",
			entry: ExternalEndpointUpstreamEntry{
				Upstream: &ExternalEndpointUpstreamSpec{URL: "https://user:sk-secret@api.example.com/v1"},
			},
			wantKind: ExternalEndpointUpstreamKindExternal,
			wantRef:  "https://api.example.com/v1",
		},
		{
			// providers commonly pass the key as a query parameter
			name: "external upstream entry with credential in query",
			entry: ExternalEndpointUpstreamEntry{
				Upstream: &ExternalEndpointUpstreamSpec{URL: "https://api.example.com/v1?api-key=sk-secret"},
			},
			wantKind: ExternalEndpointUpstreamKindExternal,
			wantRef:  "https://api.example.com/v1",
		},
		{
			name: "unparsable upstream url is not echoed back",
			entry: ExternalEndpointUpstreamEntry{
				Upstream: &ExternalEndpointUpstreamSpec{URL: "://sk-secret@nope"},
			},
			wantKind: ExternalEndpointUpstreamKindExternal,
			wantRef:  unparsableUpstreamURL,
		},
		{
			name:     "entry with neither ref nor upstream",
			entry:    ExternalEndpointUpstreamEntry{},
			wantKind: ExternalEndpointUpstreamKindExternal,
			wantRef:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantKind, tt.entry.Kind())
			assert.Equal(t, tt.wantRef, tt.entry.Ref())
			assert.Equal(t, tt.wantModels, tt.entry.ExposedModels())
			// the identity is safe to publish in status: it never carries the credential
			assert.NotContains(t, tt.entry.Ref(), "sk-secret")
		})
	}
}
