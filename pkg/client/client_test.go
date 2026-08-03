package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizesBaseURLTrailingSlash(t *testing.T) {
	tests := []struct {
		name          string
		baseURLSuffix string
		wantPath      string
	}{
		{
			name:     "without trailing slash",
			wantPath: "/api/v1/engines",
		},
		{
			name:          "with trailing slash",
			baseURLSuffix: "/",
			wantPath:      "/api/v1/engines",
		},
		{
			name:          "with path prefix and trailing slash",
			baseURLSuffix: "/neutree/",
			wantPath:      "/neutree/api/v1/engines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, tt.wantPath, r.URL.Path)
				_, err := w.Write([]byte("[]"))
				require.NoError(t, err)
			}))
			t.Cleanup(server.Close)

			client := NewClient(server.URL + tt.baseURLSuffix)
			_, err := client.Engines.List(ListOptions{})
			require.NoError(t, err)
		})
	}
}
