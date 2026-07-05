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
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hardware"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/runtimeusage"
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
DCGM_FI_DRIVER_VERSION{gpu="0",UUID="GPU-abc",modelName="A100",Driver_Version="535.104.05"} 1
DCGM_FI_CUDA_DRIVER_VERSION{gpu="0",UUID="GPU-abc",modelName="A100"} 12020
`))
	}))
	t.Cleanup(acceleratorExporter.Close)

	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporterURL:        nodeExporter.URL + "/metrics",
		AcceleratorExporterURL: acceleratorExporter.URL + "/metrics",
		HTTPClient:             nodeExporter.Client(),
		GPUHardwareProvider:    emptyGPUHardwareProvider,
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
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",source="neutree-node-agent",target="node-exporter",workspace="default"} 1`)
	assert.Contains(t, body, `# HELP neutree_accelerator_utilization_ratio Neutree node-agent metric neutree_accelerator_utilization_ratio.`)
	assert.Contains(t, body, `# TYPE neutree_accelerator_utilization_ratio gauge`)
	assert.Contains(t, body, `neutree_node_ready{cluster_type="kubernetes",neutree_cluster="k8s-a",node="node-a",node_ip="10.0.0.10",source="neutree-node-agent",workspace="default"} 1`)
	assert.Contains(t, body, `# TYPE neutree_node_memory_used_bytes gauge`)
	assert.Contains(t, body, `neutree_node_memory_used_bytes{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",source="node-exporter",workspace="default"}`)
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",neutree_cluster="k8s-a",node="node-a",product="A100",workspace="default"} 0.87`)
	assert.Contains(t, body, `neutree_node_accelerator_hardware_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",memory_total_bytes="85899345920",neutree_cluster="k8s-a",node="node-a",numa_node="unknown",pcie_bus_id="unknown",pcie_generation="unknown",pcie_width="unknown",product="A100",workspace="default"} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_nvidia_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",architecture="unknown",cluster_type="kubernetes",cuda_capability="unknown",cuda_driver_version="12.2",driver_version="535.104.05",neutree_cluster="k8s-a",node="node-a",nvlink="unknown",nvswitch="unknown",product="A100",workspace="default"} 1`)
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
		Labels: model.CanonicalLabels{
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
		GPUHardwareProvider:    emptyGPUHardwareProvider,
		AllocationProvider: allocation.ProviderFunc(func(_ context.Context, snapshot *model.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
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
						{
							UUID:          "GPU-abc",
							Product:       "NVIDIA_A100",
							MemoryMiB:     81920,
							CoreUnits:     100,
							NodeID:        "head-0",
							UsedMemoryMiB: 4096,
						},
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
	assert.Contains(t, body, `neutree_node_accelerator_total{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",product="A100",workspace="default"} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_allocated{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",product="A100",workspace="default"} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_free{accelerator_type="nvidia_gpu",cluster_type="ray",neutree_cluster="static-a",node="head-0",product="A100",workspace="default"} 0`)
	allocationLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",endpoint="chat",instance_id="actor-a",neutree_cluster="static-a",node="head-0",product="NVIDIA_A100",replica_id="replica-a",vdevice_index="0",workspace="default"`
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_allocation{`+allocationLabels+`} 1`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+allocationLabels+`}`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+allocationLabels+`}`)
	assert.Contains(t, body, `neutree_node_accelerator_allocation{`+allocationLabels+`} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_allocation_memory_allocated_bytes{`+allocationLabels+`}`)
	assert.Contains(t, body, `neutree_node_accelerator_allocation_memory_used_bytes{`+allocationLabels+`}`)
}

func TestServerMetricsIncludesDiscoveredEndpointReplicaRuntimeUsage(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
`))
	}))
	t.Cleanup(nodeExporter.Close)

	workingSetBytes := 512.0
	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:         "default",
			NeutreeCluster:    "static-a",
			StaticNodeCluster: "static-a",
			ClusterType:       "ray",
			Node:              "head-0",
			NodeIP:            "10.0.0.10",
			NodeRole:          "head",
		},
		NodeExporterURL: nodeExporter.URL + "/metrics",
		HTTPClient:      nodeExporter.Client(),
		RuntimeUsageProvider: runtimeusage.ProviderFunc(func(_ context.Context) ([]model.EndpointReplicaRuntimeUsage, error) {
			return []model.EndpointReplicaRuntimeUsage{
				{
					Workspace:             "default",
					Cluster:               "static-a",
					Endpoint:              "chat",
					InstanceID:            "actor-a",
					ReplicaID:             "replica-a",
					NodeID:                "head-0",
					Deployment:            "Backend",
					Container:             "engine",
					ContainerID:           "docker-abc",
					CPUUsageSeconds:       12.5,
					MemoryWorkingSetBytes: &workingSetBytes,
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
	runtimeLabels := `cluster_type="ray",container="engine",container_id="docker-abc",deployment="Backend",` +
		`endpoint="chat",engine="",engine_version="",instance_id="actor-a",neutree_cluster="static-a",` +
		`node="head-0",node_ip="10.0.0.10",node_role="head",replica="replica-a",` +
		`replica_id="replica-a",source="neutree-node-agent",static_node_cluster="static-a",workspace="default"`
	assert.Contains(t, body, `neutree_endpoint_replica_cpu_usage_seconds_total{`+runtimeLabels+`} 12.5`)
	assert.Contains(t, body, `neutree_endpoint_replica_memory_working_set_bytes{`+runtimeLabels+`} 512`)
}

func TestServerMetricsIncludesDiscoveredEndpointReplicaGPUUsage(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
`))
	}))
	t.Cleanup(nodeExporter.Close)

	allocatedBytes := 8192.0 * 1024 * 1024
	usedBytes := 4096.0 * 1024 * 1024
	utilization := 0.75
	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		NodeExporterURL: nodeExporter.URL + "/metrics",
		HTTPClient:      nodeExporter.Client(),
		EndpointGPUUsageProvider: fakeEndpointGPUUsageProvider{
			usages: []model.EndpointReplicaGPUUsage{
				{
					Workspace:            "default",
					Cluster:              "k8s-a",
					Endpoint:             "chat",
					InstanceID:           "chat-abc",
					ReplicaID:            "chat-abc",
					NodeID:               "node-a",
					Container:            "engine",
					GPUUUID:              "GPU-abc",
					Product:              "NVIDIA_A100",
					MemoryAllocatedBytes: &allocatedBytes,
					MemoryUsedBytes:      &usedBytes,
					UtilizationRatio:     &utilization,
				},
			},
		},
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	commonLabels := `accelerator_index="unknown",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",` +
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",neutree_cluster="k8s-a",` +
		`node="node-a",product="NVIDIA_A100",replica_id="chat-abc",vdevice_index="0",workspace="default"`
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_allocation{`+commonLabels+`} 1`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+commonLabels+`}`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+commonLabels+`}`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_utilization_ratio{`+commonLabels+`} 0.75`)
	assert.NotContains(t, body, "container=")
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
		Labels: model.CanonicalLabels{
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
		GPUHardwareProvider:    emptyGPUHardwareProvider,
		AllocationProvider: allocation.ProviderFunc(func(ctx context.Context, _ *model.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
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
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",neutree_cluster="k8s-a",node="node-a",product="A100",workspace="default"} 0.87`)
	assert.NotContains(t, body, "neutree_endpoint_replica_accelerator_allocation")
}

func TestServerMetricsKeepsSuccessfulAcceleratorExporterWhenAnotherFails(t *testing.T) {
	goodExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",modelName="A100"} 81920
`))
	}))
	t.Cleanup(goodExporter.Close)

	badExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(badExporter.Close)

	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		AcceleratorExporterURLs: []string{goodExporter.URL, badExporter.URL},
		HTTPClient:              goodExporter.Client(),
		GPUHardwareProvider:     emptyGPUHardwareProvider,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",source="neutree-node-agent",target="accelerator-exporter",workspace="default"} 1`)
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",neutree_cluster="k8s-a",node="node-a",product="A100",workspace="default"} 0.87`)
}

func TestServerNodeDeviceSnapshotDoesNotBlockOnSlowAllocationProvider(t *testing.T) {
	server, err := NewServer(Config{
		AllocationTimeout: 10 * time.Millisecond,
		AllocationProvider: allocation.ProviderFunc(func(ctx context.Context, _ *model.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
				return []v1.StaticNodeAllocationStatus{{Endpoint: "chat"}}, nil
			}
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	start := time.Now()
	resp, err := http.Get(httpServer.URL + "/v1/node/device-snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	assert.Less(t, time.Since(start), 80*time.Millisecond)
}

func TestServerWriteKubernetesAnnotationsUsesProvidedContext(t *testing.T) {
	server, err := NewServer(Config{
		AllocationProvider: allocation.ProviderFunc(func(ctx context.Context, _ *model.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
			<-ctx.Done()

			return nil, ctx.Err()
		}),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	server.writeKubernetesAnnotations(ctx)
	assert.Less(t, time.Since(start), 80*time.Millisecond)
}

func TestServerNodeDeviceSnapshotSetsMinorNumberFromHardwareInfo(t *testing.T) {
	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="7",UUID="GPU-abc",modelName="A100"} 87`))
	}))
	t.Cleanup(acceleratorExporter.Close)

	server, err := NewServer(Config{
		AcceleratorExporterURL: acceleratorExporter.URL,
		HTTPClient:             acceleratorExporter.Client(),
		GPUHardwareProvider: hardware.GPUHardwareInfoProviderFunc(func(context.Context) ([]model.GPUHardwareInfo, error) {
			return []model.GPUHardwareInfo{{UUID: "GPU-abc", MinorNumber: "3"}}, nil
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	resp, err := http.Get(httpServer.URL + "/v1/node/device-snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	var snapshot model.NodeDeviceSnapshot
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&snapshot))
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "7", snapshot.Accelerator.Devices[0].ID)
	assert.Equal(t, 3, snapshot.Accelerator.Devices[0].MinorNumber)
}

func TestServerNodeDeviceSnapshotAllowsRequests(t *testing.T) {
	server, err := NewServer(Config{
		DeviceSnapshotProvider: model.DeviceSnapshotProviderFunc(func(_ *http.Request) (*model.NodeDeviceSnapshot, error) {
			cpu := v1.CPUStaticNodeAcceleratorStatus()

			return &model.NodeDeviceSnapshot{Accelerator: cpu}, nil
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	resp, err := http.Get(httpServer.URL + "/v1/node/device-snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var snapshot model.NodeDeviceSnapshot
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&snapshot))
	assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, snapshot.Accelerator.Type)
}

func readResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	buffer, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return string(buffer)
}

var emptyGPUHardwareProvider = hardware.GPUHardwareInfoProviderFunc(func(context.Context) ([]model.GPUHardwareInfo, error) {
	return nil, nil
})

type fakeEndpointGPUUsageProvider struct {
	usages []model.EndpointReplicaGPUUsage
	err    error
}

func (p fakeEndpointGPUUsageProvider) Usages(context.Context) ([]model.EndpointReplicaGPUUsage, error) {
	return p.usages, p.err
}
