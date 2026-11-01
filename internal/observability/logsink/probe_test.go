package logsink

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestTestConnection(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte("OK"))
		case "/select/logsql/query":
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}

			assert.Equal(t, "*", r.URL.Query().Get("query"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))

	sink := func(token, ca string, insecure bool) *v1.LogSink {
		return &v1.LogSink{Spec: &v1.LogSinkSpec{VictoriaLogs: &v1.VictoriaLogsSinkSpec{
			URL: srv.URL, CACert: ca, InsecureSkipVerify: insecure,
			Auth: &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: token},
		}}}
	}

	ctx := context.Background()

	assert.NoError(t, TestConnection(ctx, sink("good", caPEM, false)))
	assert.NoError(t, TestConnection(ctx, sink("good", "", true)))

	err := TestConnection(ctx, sink("bad", caPEM, false))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated query failed")
	assert.Contains(t, err.Error(), "401")

	err = TestConnection(ctx, sink("good", "", false))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check failed")

	err = TestConnection(ctx, sink("good", "not a pem", false))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ca_cert")
}

func TestTestConnectionGuardedHealth(t *testing.T) {
	// A proxy that requires credentials on every path, /health included.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "u" || pass != "good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		if r.URL.Path == "/health" || r.URL.Path == "/select/logsql/query" {
			_, _ = w.Write([]byte("OK"))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	sink := func(pass string) *v1.LogSink {
		return &v1.LogSink{Spec: &v1.LogSinkSpec{VictoriaLogs: &v1.VictoriaLogsSinkSpec{
			URL:  srv.URL,
			Auth: &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBasic, Username: "u", Password: pass},
		}}}
	}

	ctx := context.Background()

	assert.NoError(t, TestConnection(ctx, sink("good")))

	err := TestConnection(ctx, sink("bad"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated query failed")
	assert.Contains(t, err.Error(), "401")
}
