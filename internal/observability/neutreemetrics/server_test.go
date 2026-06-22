package neutreemetrics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerHealthAndMetrics(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/metrics", r.URL.Path)
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
node_load1 2.5
`))
	}))
	t.Cleanup(nodeExporter.Close)

	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/metrics", r.URL.Path)
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 1024
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`))
	}))
	t.Cleanup(acceleratorExporter.Close)

	server, err := NewServer(Config{
		Labels: CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporterURL:        nodeExporter.URL + "/metrics",
		AcceleratorExporterURL: acceleratorExporter.URL + "/metrics",
		HTTPClient:             nodeExporter.Client(),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	healthResp, err := http.Get(httpServer.URL + "/health")
	require.NoError(t, err)
	t.Cleanup(func() { _ = healthResp.Body.Close() })
	assert.Equal(t, http.StatusOK, healthResp.StatusCode)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="",source="neutree-node-agent",static_node_cluster="",target="node-exporter",workspace="default"} 1`)
	assert.Contains(t, body, `neutree_node_memory_used_bytes{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="",source="node-exporter",static_node_cluster="",workspace="default"} 10737418240`)
	assert.Contains(t, body, `neutree_gpu_utilization_ratio{cluster_type="kubernetes",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="k8s-a",node="node-a",node_ip="10.0.0.10",node_role="",source="accelerator-exporter",static_node_cluster="",workspace="default"} 0.87`)
}

func TestServerMetricsIncludesDiscoveredEndpointAllocations(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
`))
	}))
	t.Cleanup(nodeExporter.Close)

	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`))
	}))
	t.Cleanup(acceleratorExporter.Close)

	server, err := NewServer(Config{
		Labels: CanonicalLabels{
			Workspace:         "default",
			NeutreeCluster:    "static-a",
			StaticNodeCluster: "static-a",
			ClusterType:       "ray",
			Node:              "head-0",
			NodeIP:            "10.0.0.10",
			NodeRole:          "head",
		},
		NodeExporterURL:        nodeExporter.URL + "/metrics",
		AcceleratorExporterURL: acceleratorExporter.URL + "/metrics",
		HTTPClient:             nodeExporter.Client(),
		AllocationProvider: AllocationProviderFunc(func(_ context.Context, snapshot *NodeSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
			require.Len(t, snapshot.Accelerator.Devices, 1)
			assert.Equal(t, "GPU-abc", snapshot.Accelerator.Devices[0].UUID)

			return []v1.StaticNodeAllocationStatus{
				{
					WorkloadType: "endpoint",
					Workspace:    "default",
					Endpoint:     "chat",
					InstanceID:   "actor-a",
					ReplicaID:    "replica-a",
					Devices: []v1.DeviceAllocation{
						{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 81920, CoreUnits: 100, NodeID: "head-0"},
					},
				},
			}, nil
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_endpoint_replica_gpu_allocation{cluster_type="ray",endpoint="chat",gpu_uuid="GPU-abc",instance_id="actor-a",neutree_cluster="static-a",node="head-0",node_ip="10.0.0.10",node_role="head",product="NVIDIA_A100",replica_id="replica-a",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"} 1`)
}

func TestServerMetricsDoesNotBlockOnSlowAllocationProvider(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
`))
	}))
	t.Cleanup(nodeExporter.Close)

	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`))
	}))
	t.Cleanup(acceleratorExporter.Close)

	server, err := NewServer(Config{
		Labels: CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporterURL:        nodeExporter.URL + "/metrics",
		AcceleratorExporterURL: acceleratorExporter.URL + "/metrics",
		HTTPClient:             nodeExporter.Client(),
		AllocationTimeout:      10 * time.Millisecond,
		AllocationProvider: AllocationProviderFunc(func(ctx context.Context, _ *NodeSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return []v1.StaticNodeAllocationStatus{
					{
						WorkloadType: "endpoint",
						Workspace:    "default",
						Endpoint:     "chat",
						InstanceID:   "pod-a",
						ReplicaID:    "pod-a",
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 81920, CoreUnits: 100, NodeID: "node-a"},
						},
					},
				}, nil
			}
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	start := time.Now()
	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)
	assert.Less(t, time.Since(start), 80*time.Millisecond)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_gpu_utilization_ratio{cluster_type="kubernetes",gpu_index="0",gpu_uuid="GPU-abc",model="A100",neutree_cluster="k8s-a",node="node-a",node_ip="10.0.0.10",node_role="",source="accelerator-exporter",static_node_cluster="",workspace="default"} 0.87`)
	assert.NotContains(t, body, "neutree_endpoint_replica_gpu_allocation")
}

func TestServerNodeSnapshotRequiresBearerToken(t *testing.T) {
	server, err := NewServer(Config{
		SnapshotToken: "token-a",
		SnapshotProvider: SnapshotProviderFunc(func(_ *http.Request) (*NodeSnapshot, error) {
			return &NodeSnapshot{
				Accelerator: v1.StaticNodeAcceleratorStatus{
					Type:         v1.AcceleratorTypeNVIDIAGPU.String(),
					Vendor:       "nvidia",
					ProductModel: "nvidia_gpu",
					Devices: []v1.StaticNodeAcceleratorDeviceStatus{
						{ID: "0", UUID: "GPU-abc", ProductModel: "NVIDIA_A100", MemoryMiB: 81920, Healthy: true},
					},
				},
				Allocations: []v1.StaticNodeAllocationStatus{
					{
						WorkloadType: "endpoint",
						Workspace:    "default",
						Endpoint:     "chat",
						ReplicaID:    "replica-a",
						Devices: []v1.DeviceAllocation{
							{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 81920},
						},
					},
				},
			}, nil
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	unauthorizedResp, err := http.Get(httpServer.URL + "/v1/node/snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = unauthorizedResp.Body.Close() })
	assert.Equal(t, http.StatusUnauthorized, unauthorizedResp.StatusCode)

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/v1/node/snapshot", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer token-a")

	resp, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var snapshot NodeSnapshot
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&snapshot))
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), snapshot.Accelerator.Type)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", snapshot.Accelerator.Devices[0].UUID)
	require.Len(t, snapshot.Allocations, 1)
	assert.Equal(t, "chat", snapshot.Allocations[0].Endpoint)
}

func TestServerNodeSnapshotAllowsRequestsWhenBearerTokenNotConfigured(t *testing.T) {
	server, err := NewServer(Config{
		SnapshotProvider: SnapshotProviderFunc(func(_ *http.Request) (*NodeSnapshot, error) {
			cpu := v1.CPUStaticNodeAcceleratorStatus()

			return &NodeSnapshot{Accelerator: cpu}, nil
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	resp, err := http.Get(httpServer.URL + "/v1/node/snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var snapshot NodeSnapshot
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&snapshot))
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, snapshot.Accelerator.Type)
}

func readResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	buffer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(buffer)
}
