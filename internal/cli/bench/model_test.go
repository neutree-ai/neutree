package bench

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFirstModelName(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
		wantErr string
	}{
		{
			name:    "OpenAI model id",
			payload: `{"data":[{"id":"model-a"}]}`,
			want:    "model-a",
		},
		{
			name:    "Neutree model name",
			payload: `{"data":[{"name":"Qwen/Qwen3.8-27B-FP8"}]}`,
			want:    "Qwen/Qwen3.8-27B-FP8",
		},
		{
			name:    "first model wins",
			payload: `{"data":[{"name":"first"},{"id":"second"}]}`,
			want:    "first",
		},
		{
			name:    "missing data",
			payload: `{"object":"list"}`,
			wantErr: "data",
		},
		{
			name:    "invalid response JSON",
			payload: `not JSON`,
			wantErr: "decode /v1/models response",
		},
		{
			name:    "data is not an array",
			payload: `{"data":{}}`,
			wantErr: "decode /v1/models data",
		},
		{
			name:    "empty data",
			payload: `{"data":[]}`,
			wantErr: "empty data",
		},
		{
			name:    "missing model identifier",
			payload: `{"data":[{"object":"model"}]}`,
			wantErr: "id, name, or model",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := firstModelName([]byte(test.payload))
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestModelDiscoveryCommandUsesRoutedModelsURL(t *testing.T) {
	command := modelDiscoveryCommand("http://router-service.ns.svc.cluster.local:8000/default/chat")

	require.Contains(t, command, "python3 -c")
	require.Contains(t, command, "/v1/models")
	require.Contains(t, command, "router-service.ns.svc.cluster.local")
}
