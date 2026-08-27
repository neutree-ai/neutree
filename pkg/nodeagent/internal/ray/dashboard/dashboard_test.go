package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientUsesReadOnlyNodeAndServeEndpoints(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/nodes":
			assert.Equal(t, http.MethodGet, request.Method)
			assert.Equal(t, "summary", request.URL.Query().Get("view"))
			requests = append(requests, request.URL.RequestURI())
			_, _ = writer.Write([]byte(`{"data":{"summary":[]}}`))
		case "/api/serve/applications/":
			assert.Equal(t, http.MethodGet, request.Method)
			requests = append(requests, request.URL.RequestURI())
			_, _ = writer.Write([]byte(`{"applications":{"default_chat":{"status":"RUNNING"}},"proxies":{}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)

	client := &Client{dashboardURL: server.URL, client: server.Client()}
	nodes, err := client.ListNodes()
	require.NoError(t, err)
	assert.Empty(t, nodes)

	applications, err := client.GetServeApplications()
	require.NoError(t, err)
	assert.Equal(t, ApplicationStatusRunning, applications.Applications["default_chat"].Status)
	assert.Equal(t, []string{"/nodes?view=summary", "/api/serve/applications/"}, requests)
}

func TestClientListActorsBuildsFilterQueryAndParsesResponse(t *testing.T) {
	var capturedQuery map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assert.Equal(t, http.MethodGet, request.Method)
		assert.Equal(t, "/api/v0/actors", request.URL.Path)
		capturedQuery = request.URL.Query()
		_, _ = writer.Write([]byte(`{"result":true,"data":{"result":{"total":1,"num_after_truncation":1,"num_filtered":1,"result":[{"actor_id":"actor-a","node_id":"node-a","pid":1234}]}}}`))
	}))
	t.Cleanup(server.Close)

	client := &Client{dashboardURL: server.URL, client: server.Client()}
	response, err := client.ListActors(
		[]ActorFilter{{Key: "class_name", Predicate: "=", Value: "ServeReplica:default_chat:chat"}},
		true,
		1,
	)

	require.NoError(t, err)
	require.Len(t, response.Data.Result.Result, 1)
	assert.Equal(t, "actor-a", response.Data.Result.Result[0].ActorID)
	assert.Equal(t, 1234, response.Data.Result.Result[0].PID)
	assert.Equal(t, []string{"class_name"}, capturedQuery["filter_keys"])
	assert.Equal(t, []string{"="}, capturedQuery["filter_predicates"])
	assert.Equal(t, []string{"ServeReplica:default_chat:chat"}, capturedQuery["filter_values"])
	assert.Equal(t, []string{"true"}, capturedQuery["detail"])
	assert.Equal(t, []string{"1"}, capturedQuery["limit"])
}

func TestClientWrapsReadOnlyEndpointErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	client := &Client{dashboardURL: server.URL, client: server.Client()}
	applications, err := client.GetServeApplications()

	require.Error(t, err)
	assert.Nil(t, applications)
	assert.ErrorContains(t, err, "failed to execute request to get serve applications")
}
