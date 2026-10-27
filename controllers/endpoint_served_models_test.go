package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	orchestratormocks "github.com/neutree-ai/neutree/internal/orchestrator/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestFetchServedModels(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    []string
		wantErr bool
	}{
		{name: "ordered unique models", body: `{"data":[{"id":"alias-b"},{"id":"alias-a"},{"id":"alias-b"}]}`, want: []string{"alias-b", "alias-a"}},
		{name: "empty list", body: `{"data":[]}`, want: []string{}},
		{name: "keeps exact model identity", body: `{"data":[{"id":" model "}]}`, want: []string{" model "}},
		{name: "server failure", status: http.StatusServiceUnavailable, body: `{"data":[]}`, wantErr: true},
		{name: "malformed json", body: `{`, wantErr: true},
		{name: "missing collection", body: `{}`, wantErr: true},
		{name: "null collection", body: `{"data":null}`, wantErr: true},
		{name: "empty id", body: `{"data":[{"id":""}]}`, wantErr: true},
		{name: "whitespace id", body: `{"data":[{"id":"  "}]}`, wantErr: true},
		{name: "non-string id", body: `{"data":[{"id":3}]}`, wantErr: true},
		{name: "null model", body: `{"data":[null]}`, wantErr: true},
		{name: "trailing json", body: `{"data":[]} {}`, wantErr: true},
		{name: "oversized response", body: `{"data":[{"id":"` + strings.Repeat("x", 1<<20) + `"}]}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/team%2Fa/model%3Fname/v1/models", r.URL.EscapedPath())
				assert.Equal(t, http.MethodGet, r.Method)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "team/a", Name: "model?name"}}
			got, err := fetchServedModels(cluster, endpoint)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFetchServedModelsDoesNotFollowRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("model discovery followed a redirect")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}
	_, err := fetchServedModels(cluster, ep(1, v1.EndpointPhaseRUNNING))
	require.Error(t, err)
}

func TestEndpointServedModelsStatus(t *testing.T) {
	c := &EndpointController{}
	endpoint := ep(1, v1.EndpointPhaseRUNNING)
	endpoint.Status.ServedModels = []string{"first", "second"}
	cases := []struct {
		name    string
		models  []string
		want    []string
		changed bool
	}{
		{name: "preserve observation when not refreshed", models: nil, want: []string{"first", "second"}},
		{name: "same models", models: []string{"first", "second"}, want: []string{"first", "second"}},
		{name: "first model changed", models: []string{"second", "first"}, want: []string{"second", "first"}, changed: true},
		{name: "successful empty collection", models: []string{}, want: []string{}, changed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServedModels: tc.models}
			assert.Equal(t, tc.changed, c.shouldUpdateStatus(endpoint, status))
			c.prepareStatusForUpdate(endpoint, status)
			assert.Equal(t, tc.want, status.ServedModels)
		})
	}
	assert.False(t, c.shouldUpdateStatus(ep(1, v1.EndpointPhaseRUNNING), &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServedModels: []string{}}))
}

func TestEndpointControllerObservesServedModels(t *testing.T) {
	original := orchestrator.NewOrchestrator
	t.Cleanup(func() { orchestrator.NewOrchestrator = original })
	cases := []struct {
		name     string
		phase    v1.EndpointPhase
		body     string
		want     []string
		requests int
	}{
		{name: "refresh running", phase: v1.EndpointPhaseRUNNING, body: `{"data":[{"id":"runtime-alias"}]}`, want: []string{"runtime-alias"}, requests: 1},
		{name: "transient failure retains observation", phase: v1.EndpointPhaseRUNNING, body: `{`, want: []string{"previous"}, requests: 1},
		{name: "successful empty clears observation", phase: v1.EndpointPhaseRUNNING, body: `{"data":[]}`, want: []string{}, requests: 1},
		{name: "paused does not probe", phase: v1.EndpointPhasePAUSED, want: []string{"previous"}},
		{name: "deploying does not probe", phase: v1.EndpointPhaseDEPLOYING, want: []string{"previous"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(t, "/default/test-endpoint-1/v1/models", r.URL.Path)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			store := &storagemocks.MockStorage{}
			store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}}, nil)
			o := &orchestratormocks.MockOrchestrator{}
			o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{Phase: tc.phase}, nil)
			c := newTestEndpointController(store, o)
			endpoint := ep(1, v1.EndpointPhaseRUNNING)
			endpoint.Status.ServedModels = []string{"previous"}
			status, err := c.getActualStatus(endpoint)
			require.NoError(t, err)
			c.prepareStatusForUpdate(endpoint, status)
			assert.Equal(t, tc.phase, status.Phase)
			assert.Equal(t, tc.want, status.ServedModels)
			assert.Equal(t, tc.requests, requests)
			store.AssertExpectations(t)
			o.AssertExpectations(t)
		})
	}
}

func TestFetchServedModelsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}
	start := time.Now()
	_, err := fetchServedModels(cluster, ep(1, v1.EndpointPhaseRUNNING))
	require.Error(t, err)
	assert.Less(t, time.Since(start), 8*time.Second)
}
