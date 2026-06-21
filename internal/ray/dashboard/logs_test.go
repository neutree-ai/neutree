package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_GetActorLog_BuildsLogFileQuery(t *testing.T) {
	var capturedQuery map[string][]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v0/logs/file", r.URL.Path)
		capturedQuery = r.URL.Query()
		_, _ = w.Write([]byte("NEUTREE_MODEL_DOWNLOAD_START\n"))
	}))
	defer srv.Close()

	c := &Client{dashboardURL: srv.URL, client: &http.Client{}}

	got, err := c.GetActorLog("actor-1", "out", 200)

	require.NoError(t, err)
	assert.Equal(t, "NEUTREE_MODEL_DOWNLOAD_START\n", got)
	assert.Equal(t, []string{"actor-1"}, capturedQuery["actor_id"])
	assert.Equal(t, []string{"out"}, capturedQuery["suffix"])
	assert.Equal(t, []string{"text"}, capturedQuery["format"])
	assert.Equal(t, []string{"200"}, capturedQuery["lines"])
}

func TestClient_GetActorLog_NonOKStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Client{dashboardURL: srv.URL, client: &http.Client{}}

	got, err := c.GetActorLog("actor-1", "err", 0)

	require.Error(t, err)
	assert.Empty(t, got)
	assert.Contains(t, err.Error(), "500")
}
