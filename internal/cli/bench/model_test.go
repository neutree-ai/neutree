package bench

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestFirstServedModel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint *v1.Endpoint
		want     string
		wantErr  string
	}{
		{
			name: "uses the first served model",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "chat"},
				Status:   &v1.EndpointStatus{ServedModels: []string{"router-model", "alias"}},
			},
			want: "router-model",
		},
		{
			name: "reports an endpoint without served models",
			endpoint: &v1.Endpoint{
				Metadata: &v1.Metadata{Name: "chat"},
				Status:   &v1.EndpointStatus{},
			},
			wantErr: `Endpoint "chat" reports no served models`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := firstServedModel(tc.endpoint)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
