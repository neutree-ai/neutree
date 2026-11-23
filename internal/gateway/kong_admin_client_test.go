package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"
)

// A Kong peer that never answers (e.g. a pooled connection to a killed pod)
// must fail the call within the response-header timeout, for reads and writes.
func TestKongAdminClientFailsFastWhenKongNeverAnswers(t *testing.T) {
	hang := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-hang
	}))
	defer server.Close()
	defer close(hang) // release hung handlers before server.Close waits on them

	const requestTimeout = 2 * time.Second

	client, err := kong.NewClient(pointy.String(server.URL), newKongAdminHTTPClient(requestTimeout, 100*time.Millisecond))
	require.NoError(t, err)

	calls := map[string]func() error{
		"read": func() error {
			_, err := client.Services.Get(context.Background(), pointy.String("svc"))
			return err
		},
		"write": func() error {
			_, err := client.Services.Create(context.Background(), &kong.Service{Name: pointy.String("svc")})
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			err := call()

			var netErr net.Error
			require.True(t, errors.As(err, &netErr) && netErr.Timeout(), "want a timeout error, got %v", err)
			assert.Less(t, time.Since(start), requestTimeout)
		})
	}
}
