package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadRequestsRespectContextCancellation(t *testing.T) {
	tests := []struct {
		name string
		path string
		read func(context.Context, *Client) error
	}{
		{
			name: "generic resource",
			path: "/api/v1/endpoints",
			read: func(ctx context.Context, client *Client) error {
				_, err := client.Generic.GetContext(ctx, "Endpoint", "default", "endpoint-a")
				return err
			},
		},
		{
			name: "cluster credentials",
			path: "/api/v1/credentials/clusters",
			read: func(ctx context.Context, client *Client) error {
				_, err := client.GetClusterCredentialsContext(ctx, "default", "cluster-a")
				return err
			},
		},
	}

	stages := []struct {
		name        string
		partialBody bool
	}{
		{name: "waiting for response"},
		{name: "reading response body", partialBody: true},
	}

	for _, tt := range tests {
		for _, stage := range stages {
			t.Run(tt.name+"/"+stage.name, func(t *testing.T) {
				started := make(chan *http.Request, 1)
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					if stage.partialBody {
						_, _ = writer.Write([]byte("["))
						_ = http.NewResponseController(writer).Flush()
					}
					started <- request
					select {
					case <-request.Context().Done():
					case <-release:
					}
				}))
				defer server.Close()
				defer close(release)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					result <- tt.read(ctx, NewClient(server.URL))
				}()

				select {
				case request := <-started:
					require.Equal(t, tt.path, request.URL.Path)
				case <-time.After(5 * time.Second):
					t.Fatal("request did not reach the server")
				}

				cancel()
				select {
				case err := <-result:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(5 * time.Second):
					t.Fatal("request did not stop after context cancellation")
				}
			})
		}
	}
}
