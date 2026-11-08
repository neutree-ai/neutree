package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var testKongAdminTimeouts = kongAdminTimeouts{
	Request:        2 * time.Second,
	Dial:           500 * time.Millisecond,
	ResponseHeader: 300 * time.Millisecond,
	IdleConn:       time.Second,
}

// silentListener accepts TCP connections and never reads or writes, like a
// peer that vanished without sending FIN/RST.
func silentListener(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu    sync.Mutex
		conns []net.Conn
	)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()

		mu.Lock()
		defer mu.Unlock()

		for _, c := range conns {
			_ = c.Close()
		}
	})

	return "http://" + ln.Addr().String()
}

func TestKongAdminClientDefaults(t *testing.T) {
	d := defaultKongAdminTimeouts
	assert.Equal(t, 10*time.Second, d.Request)
	assert.Equal(t, 3*time.Second, d.Dial)
	assert.Equal(t, 3*time.Second, d.ResponseHeader)
	assert.Equal(t, 30*time.Second, d.IdleConn)
	// One timed-out attempt plus a retry on a fresh connection fits the cap.
	assert.LessOrEqual(t, 2*d.ResponseHeader+d.Dial, d.Request)
	// A call that hit the cap is retried by the next reconcile tick (10s).
	assert.LessOrEqual(t, d.Request, 10*time.Second)
}

func TestKongAdminClientReturnsWhenPeerNeverAnswers(t *testing.T) {
	url := silentListener(t)
	client := newKongAdminHTTPClient(testKongAdminTimeouts)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), method, url+"/consumers", nil)
			require.NoError(t, err)

			start := time.Now()
			resp, err := client.Do(req) //nolint:bodyclose
			elapsed := time.Since(start)

			require.Error(t, err)
			assert.Nil(t, resp)
			assert.True(t, isTimeout(err), "want a timeout error, got %v", err)
			assert.Less(t, elapsed, testKongAdminTimeouts.Request)
		})
	}
}

func TestKongAdminClientRetriesIdempotentCallOnce(t *testing.T) {
	var calls atomic.Int32

	hang := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-hang // the first attempt never gets an answer
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"svc-1","name":"svc"}`))
	}))
	defer server.Close()
	defer close(hang) // release hung handlers before server.Close waits on them

	client, err := kong.NewClient(pointy.String(server.URL), newKongAdminHTTPClient(testKongAdminTimeouts))
	require.NoError(t, err)

	svc, err := client.Services.Get(context.Background(), pointy.String("svc"))
	require.NoError(t, err)
	assert.Equal(t, "svc-1", *svc.ID)
	assert.Equal(t, int32(2), calls.Load())
}

func TestKongAdminClientDoesNotRetryWrites(t *testing.T) {
	var calls atomic.Int32

	hang := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-hang
	}))
	defer server.Close()
	defer close(hang) // release hung handlers before server.Close waits on them

	client, err := kong.NewClient(pointy.String(server.URL), newKongAdminHTTPClient(testKongAdminTimeouts))
	require.NoError(t, err)

	_, err = client.Services.Create(context.Background(), &kong.Service{Name: pointy.String("svc")})
	require.Error(t, err)
	assert.Equal(t, int32(1), calls.Load())
}

// A timed-out write fails the sync instead of hanging it, and the next
// reconcile of the same key completes.
func TestSyncAPIKeyRecoversAfterTimedOutCall(t *testing.T) {
	apiKey := &v1.ApiKey{ID: "key-1", Status: &v1.ApiKeyStatus{SkValue: "sk-1"}}

	var (
		consumerPosts atomic.Int32
		created       atomic.Bool
	)

	hang := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/consumers":
			if created.Load() {
				_, _ = w.Write([]byte(`{"data":[{"id":"consumer-1","custom_id":"key-1"}]}`))
				return
			}

			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/consumers":
			if consumerPosts.Add(1) == 1 {
				<-hang
				return
			}

			created.Store(true)
			_, _ = w.Write([]byte(`{"id":"consumer-1","custom_id":"key-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/key-auth":
			_, _ = w.Write([]byte(`{"data":[{"key":"sk-1"}]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/consumers/consumer-1/"):
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/plugins/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/plugins":
			_, _ = w.Write([]byte(`{"id":"plugin-1"}`))
		default:
			t.Errorf("unexpected Kong request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	defer close(hang) // release hung handlers before server.Close waits on them

	client, err := kong.NewClient(pointy.String(server.URL), newKongAdminHTTPClient(testKongAdminTimeouts))
	require.NoError(t, err)

	k := &Kong{kongClient: client}

	start := time.Now()
	err = k.SyncAPIKey(apiKey)
	require.Error(t, err)
	assert.Less(t, time.Since(start), testKongAdminTimeouts.Request)

	require.NoError(t, k.SyncAPIKey(apiKey))
	assert.Equal(t, int32(2), consumerPosts.Load())
}
