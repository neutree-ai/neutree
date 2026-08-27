package neutreemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/accelerator"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/hardware"
	metricskubernetes "github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/model"
	metricsnormalizer "github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/runtimeusage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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
		ScrapeTargetProvider: testTargetProvider(
			nodeExporter.URL+"/metrics",
			acceleratorExporter.URL+"/metrics",
		),
		HTTPClient:          nodeExporter.Client(),
		GPUHardwareProvider: emptyGPUHardwareProvider,
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
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="neutree-node-agent",target="node-exporter"} 1`)
	assert.Contains(t, body, `# HELP neutree_accelerator_utilization_ratio Neutree node-agent metric neutree_accelerator_utilization_ratio.`)
	assert.Contains(t, body, `# TYPE neutree_accelerator_utilization_ratio gauge`)
	assert.Contains(t, body, `neutree_node_ready{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="neutree-node-agent"} 1`)
	assert.Contains(t, body, `# TYPE neutree_node_memory_used_bytes gauge`)
	assert.Contains(t, body, `neutree_node_memory_used_bytes{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="node-exporter"}`)
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",node="node-a",product="A100"} 0.87`)
	assert.Contains(t, body, `neutree_node_accelerator_hardware_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",memory_total_bytes="85899345920",node="node-a",numa_node="unknown",pcie_bus_id="unknown",pcie_generation="unknown",pcie_width="unknown",product="A100"} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_nvidia_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",architecture="unknown",cluster_type="kubernetes",cuda_capability="unknown",cuda_driver_version="12.2",driver_version="535.104.05",node="node-a",nvlink="unknown",nvswitch="unknown",product="A100"} 1`)
}

func TestNewServerRejectsUnregisteredAcceleratorType(t *testing.T) {
	testCases := []struct {
		name         string
		accelerators map[string]adapter.Accelerator
	}{
		{
			name: "missing adapter",
		},
		{
			name: "nil adapter",
			accelerators: map[string]adapter.Accelerator{
				"unknown-accelerator": nil,
			},
		},
		{
			name: "typed nil adapter",
			accelerators: map[string]adapter.Accelerator{
				"unknown-accelerator": (*typedNilAccelerator)(nil),
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewServer(Config{
				AcceleratorType: "unknown-accelerator",
				Accelerators:    testCase.accelerators,
			})

			assert.ErrorContains(t, err, "accelerator adapter \"unknown-accelerator\" is not registered")
		})
	}
}

func TestServerMetricsRoutesThroughSelectedAcceleratorAdapter(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184`))
	}))
	t.Cleanup(nodeExporter.Close)

	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 1024
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
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		Accelerators:    map[string]adapter.Accelerator{v1.AcceleratorTypeNVIDIAGPU.String(): sampleKubernetesAccelerator{}},
		ScrapeTargetProvider: testTargetProvider(
			nodeExporter.URL+"/metrics",
			acceleratorExporter.URL+"/metrics",
		),
		HTTPClient:          nodeExporter.Client(),
		GPUHardwareProvider: emptyGPUHardwareProvider,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",node="node-a",product="A100"} 0.87`)
	assert.Contains(t, body, `neutree_node_accelerator_total{accelerator_type="nvidia_gpu",cluster_type="kubernetes",node="node-a",product="A100"} 1`)
}

func TestServerMetricsWithAcceleratorTypeButNoExporterSkipsAcceleratorSamples(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184`))
	}))
	t.Cleanup(nodeExporter.Close)

	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		Accelerators:    map[string]adapter.Accelerator{v1.AcceleratorTypeNVIDIAGPU.String(): sampleKubernetesAccelerator{}},
		ScrapeTargetProvider: testTargetProvider(
			nodeExporter.URL + "/metrics",
		),
		HTTPClient:          nodeExporter.Client(),
		GPUHardwareProvider: emptyGPUHardwareProvider,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_node_ready{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="neutree-node-agent"} 1`)
	assert.NotContains(t, body, "neutree_accelerator_utilization_ratio")
	assert.NotContains(t, body, "neutree_node_accelerator_total")
}

func TestServerMetricsDoesNotFallBackToLegacyWhenAdapterErrors(t *testing.T) {
	nodeExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184`))
	}))
	t.Cleanup(nodeExporter.Close)

	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",modelName="A100"} 1024
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
		AcceleratorType: "nvidia_gpu",
		Accelerators: map[string]adapter.Accelerator{
			"nvidia_gpu": failingAccelerator{},
		},
		ScrapeTargetProvider: testTargetProvider(
			nodeExporter.URL+"/metrics",
			acceleratorExporter.URL+"/metrics",
		),
		HTTPClient:          nodeExporter.Client(),
		GPUHardwareProvider: emptyGPUHardwareProvider,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })

	body := readResponseBody(t, metricsResp)
	// A failing adapter must disable accelerator samples entirely, not fall
	// back to parsing the DCGM body through the legacy path.
	assert.NotContains(t, body, "neutree_accelerator_utilization_ratio")
	assert.NotContains(t, body, "neutree_node_accelerator_total")
}

type failingAccelerator struct{}

func (failingAccelerator) Type() string { return "nvidia_gpu" }

func (failingAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{}, nil
}

func (failingAccelerator) BuildKubernetesMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, fmt.Errorf("adapter boom")
}

type sampleKubernetesAccelerator struct{}

func (sampleKubernetesAccelerator) Type() string { return v1.AcceleratorTypeNVIDIAGPU.String() }

func (sampleKubernetesAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{{
			ID:           "0",
			UUID:         "GPU-abc",
			ProductName:  "A100",
			ProductModel: "A100",
			Healthy:      true,
		}},
	}}, nil
}

func (sampleKubernetesAccelerator) BuildKubernetesMetrics(
	_ context.Context,
	_ adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	if !evidence.Common.ExporterUp {
		return adapter.MetricResult{}, nil
	}
	physicalLabels := map[string]string{
		"cluster_type":      evidence.Common.Labels.ClusterType,
		"node":              evidence.Common.Labels.Node,
		"accelerator_type":  v1.AcceleratorTypeNVIDIAGPU.String(),
		"accelerator_uuid":  "GPU-abc",
		"accelerator_index": "0",
		"product":           "A100",
	}
	nodeLabels := map[string]string{
		"cluster_type":     evidence.Common.Labels.ClusterType,
		"node":             evidence.Common.Labels.Node,
		"accelerator_type": v1.AcceleratorTypeNVIDIAGPU.String(),
		"product":          "A100",
	}
	return adapter.MetricResult{Samples: []adapter.Sample{
		{Name: "neutree_accelerator_utilization_ratio", Labels: physicalLabels, Value: 0.87},
		{Name: "neutree_node_accelerator_total", Labels: nodeLabels, Value: 1},
	}}, nil
}

type typedNilAccelerator struct{}

func (*typedNilAccelerator) Type() string { return "unknown-accelerator" }

func (*typedNilAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{}, nil
}

type capabilityTestAccelerator struct {
	typ string
}

func (a capabilityTestAccelerator) Type() string { return a.typ }

func (capabilityTestAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{}, nil
}

type staticCapabilityTestAccelerator struct {
	capabilityTestAccelerator
}

func (staticCapabilityTestAccelerator) BuildStaticMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, nil
}

type kubernetesCapabilityTestAccelerator struct {
	capabilityTestAccelerator
}

func (kubernetesCapabilityTestAccelerator) BuildKubernetesMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, nil
}

type recordingKubernetesAccelerator struct {
	capabilityTestAccelerator
	builds   int
	hardware adapter.HardwareSnapshot
	evidence adapter.KubernetesEvidence
}

func (a *recordingKubernetesAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	if a.hardware.Accelerator.Type != "" {
		return a.hardware.Clone(), nil
	}

	return adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{Type: a.typ}}, nil
}

func (a *recordingKubernetesAccelerator) BuildKubernetesMetrics(
	_ context.Context,
	_ adapter.HardwareSnapshot,
	evidence adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	a.builds++
	a.evidence = evidence

	return adapter.MetricResult{}, nil
}

type recordingStaticAccelerator struct {
	capabilityTestAccelerator
	discoveries int
	builds      int
	exporterUp  bool
	hardware    adapter.HardwareSnapshot
	allocations []v1.StaticNodeAllocationStatus
	evidence    adapter.StaticEvidence
}

func (a *recordingStaticAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	a.discoveries++
	if a.hardware.Accelerator.Type != "" {
		return a.hardware.Clone(), nil
	}

	return adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{Type: a.typ}}, nil
}

func (a *recordingStaticAccelerator) BuildStaticMetrics(
	_ context.Context,
	_ adapter.HardwareSnapshot,
	evidence adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	a.builds++
	a.exporterUp = evidence.Common.ExporterUp
	a.evidence = evidence

	return adapter.MetricResult{Allocations: a.allocations}, nil
}

type fakeKubernetesAcceleratorEvidenceProvider struct {
	evidence adapter.KubernetesEvidence
	err      error
}

func (p fakeKubernetesAcceleratorEvidenceProvider) KubernetesAcceleratorEvidence(
	context.Context,
) (adapter.KubernetesEvidence, error) {
	return p.evidence, p.err
}

type fakeStaticAcceleratorEvidenceProvider struct {
	evidence adapter.StaticEvidence
	err      error
}

func (p fakeStaticAcceleratorEvidenceProvider) StaticAcceleratorEvidence(
	context.Context,
) (adapter.StaticEvidence, error) {
	return p.evidence, p.err
}

func TestNewServerRejectsAdapterWithoutRequestedClusterCapability(t *testing.T) {
	testCases := []struct {
		name        string
		clusterType string
		accelerator adapter.Accelerator
		expected    string
	}{
		{
			name:        "static only on Kubernetes",
			clusterType: "kubernetes",
			accelerator: staticCapabilityTestAccelerator{capabilityTestAccelerator{typ: "vendor"}},
			expected:    "does not implement Kubernetes capability",
		},
		{
			name:        "Kubernetes only on static",
			clusterType: "ray",
			accelerator: kubernetesCapabilityTestAccelerator{capabilityTestAccelerator{typ: "vendor"}},
			expected:    "does not implement static capability",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := NewServer(Config{
				ClusterType:     testCase.clusterType,
				AcceleratorType: "vendor",
				Accelerators: map[string]adapter.Accelerator{
					"vendor": testCase.accelerator,
				},
			})

			assert.ErrorContains(t, err, testCase.expected)
		})
	}
}

func TestServerDiscoversHardwareWhenExplicitStaticExporterIsUnavailable(t *testing.T) {
	accelerator := &recordingStaticAccelerator{capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"}}
	server, err := NewServer(Config{
		ClusterType:     "ray",
		AcceleratorType: "vendor",
		Accelerators: map[string]adapter.Accelerator{
			"vendor": accelerator,
		},
		ScrapeTargetProvider: staticTestTargetProvider{},
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	response, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	assert.Equal(t, http.StatusOK, response.StatusCode)
	assert.Equal(t, 1, accelerator.discoveries)
	assert.Equal(t, 1, accelerator.builds)
	assert.False(t, accelerator.exporterUp)
}

func TestServerPassesKubernetesRawEvidenceToExplicitAdapter(t *testing.T) {
	accelerator := &recordingKubernetesAccelerator{
		capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"},
	}
	server, err := NewServer(Config{
		ClusterType:     "kubernetes",
		AcceleratorType: "vendor",
		Labels:          model.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"},
		Accelerators: map[string]adapter.Accelerator{
			"vendor": accelerator,
		},
		KubernetesAcceleratorEvidenceProvider: fakeKubernetesAcceleratorEvidenceProvider{
			evidence: adapter.KubernetesEvidence{
				AllocationAvailable: true,
				PodResources: []adapter.PodResource{{
					Namespace: "default",
					Name:      "pod-a",
				}},
				EndpointPods: []adapter.EndpointPodEvidence{{
					Namespace:   "default",
					Name:        "pod-a",
					UID:         "uid-a",
					NodeName:    "node-a",
					Labels:      map[string]string{"endpoint": "chat"},
					Annotations: map[string]string{"hami.io/example": "raw"},
				}},
			},
		},
	})
	require.NoError(t, err)

	hardware, err := server.discoverAdapterHardware(context.Background(), accelerator)
	require.NoError(t, err)
	usedBytes := 1024.0

	_, err = server.adapterMetricResult(context.Background(), accelerator, hardware, nil, []model.EndpointReplicaGPUUsage{{
		Endpoint:        "chat",
		GPUUUID:         "GPU-abc",
		MemoryUsedBytes: &usedBytes,
	}})
	require.NoError(t, err)
	require.Equal(t, 1, accelerator.builds)
	assert.True(t, accelerator.evidence.AllocationAvailable)
	require.Len(t, accelerator.evidence.PodResources, 1)
	assert.Equal(t, "pod-a", accelerator.evidence.PodResources[0].Name)
	require.Len(t, accelerator.evidence.EndpointPods, 1)
	assert.Equal(t, "raw", accelerator.evidence.EndpointPods[0].Annotations["hami.io/example"])
	assert.Equal(t, "kubernetes", accelerator.evidence.Common.Labels.ClusterType)
	require.Len(t, accelerator.evidence.Common.EndpointReplicaAcceleratorUsages, 1)
	assert.Equal(t, "GPU-abc", accelerator.evidence.Common.EndpointReplicaAcceleratorUsages[0].AcceleratorUUID)
	assert.Equal(t, 1024.0, *accelerator.evidence.Common.EndpointReplicaAcceleratorUsages[0].MemoryUsedBytes)
}

func TestServerPassesStaticRawEvidenceToExplicitAdapter(t *testing.T) {
	accelerator := &recordingStaticAccelerator{
		capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"},
	}
	server, err := NewServer(Config{
		ClusterType:     "ray",
		AcceleratorType: "vendor",
		Labels:          model.CanonicalLabels{ClusterType: "ray", Node: "head-0"},
		Accelerators: map[string]adapter.Accelerator{
			"vendor": accelerator,
		},
		StaticAcceleratorEvidenceProvider: fakeStaticAcceleratorEvidenceProvider{
			evidence: adapter.StaticEvidence{
				AllocationAvailable: true,
				RayEvidence: adapter.RayEvidence{
					Actors: []adapter.RayActor{{
						ActorID: "actor-a",
						PID:     4321,
					}},
					ActorProcesses: map[int]adapter.ProcessInfo{
						4321: {
							PID:         4321,
							ParentPID:   1234,
							Environment: map[string]string{"CUDA_VISIBLE_DEVICES": "0"},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	hardware, err := server.discoverAdapterHardware(context.Background(), accelerator)
	require.NoError(t, err)

	_, err = server.adapterMetricResult(context.Background(), accelerator, hardware, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, accelerator.builds)
	assert.True(t, accelerator.evidence.AllocationAvailable)
	require.Len(t, accelerator.evidence.RayEvidence.Actors, 1)
	assert.Equal(t, "actor-a", accelerator.evidence.RayEvidence.Actors[0].ActorID)
	assert.Equal(t, "0", accelerator.evidence.RayEvidence.ActorProcesses[4321].Environment["CUDA_VISIBLE_DEVICES"])
	assert.Equal(t, "ray", accelerator.evidence.Common.Labels.ClusterType)
}

func TestServerDegradesWhenStaticEvidenceCollectionFails(t *testing.T) {
	accelerator := &recordingStaticAccelerator{
		capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"},
	}
	server, err := NewServer(Config{
		ClusterType:     "ray",
		AcceleratorType: "vendor",
		Labels:          model.CanonicalLabels{ClusterType: "ray", Node: "head-0"},
		Accelerators: map[string]adapter.Accelerator{
			"vendor": accelerator,
		},
		StaticAcceleratorEvidenceProvider: fakeStaticAcceleratorEvidenceProvider{
			err: fmt.Errorf("ray evidence unavailable"),
		},
	})
	require.NoError(t, err)

	hardware, err := server.discoverAdapterHardware(context.Background(), accelerator)
	require.NoError(t, err)

	_, err = server.adapterMetricResult(context.Background(), accelerator, hardware, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, accelerator.builds)
	assert.False(t, accelerator.evidence.AllocationAvailable)
	assert.Empty(t, accelerator.evidence.RayEvidence.Actors)
}

func TestServerDegradesWhenKubernetesEvidenceCollectionFails(t *testing.T) {
	accelerator := &recordingKubernetesAccelerator{
		capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"},
	}
	server, err := NewServer(Config{
		ClusterType:     "kubernetes",
		AcceleratorType: "vendor",
		Labels:          model.CanonicalLabels{ClusterType: "kubernetes", Node: "node-a"},
		Accelerators: map[string]adapter.Accelerator{
			"vendor": accelerator,
		},
		KubernetesAcceleratorEvidenceProvider: fakeKubernetesAcceleratorEvidenceProvider{
			err: fmt.Errorf("pod resources unavailable"),
		},
	})
	require.NoError(t, err)

	hardware, err := server.discoverAdapterHardware(context.Background(), accelerator)
	require.NoError(t, err)

	_, err = server.adapterMetricResult(context.Background(), accelerator, hardware, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, accelerator.builds)
	assert.False(t, accelerator.evidence.AllocationAvailable)
	assert.Empty(t, accelerator.evidence.PodResources)
	assert.Empty(t, accelerator.evidence.EndpointPods)
}

func TestServerNodeDeviceSnapshotUsesExplicitAdapterHardwareAndAllocations(t *testing.T) {
	accelerator := &recordingStaticAccelerator{
		capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"},
		hardware: adapter.HardwareSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{
				Type: "vendor",
				Devices: []v1.StaticNodeAcceleratorDeviceStatus{{
					ID:           "0",
					UUID:         "vendor-0",
					ProductName:  "Vendor L20",
					ProductModel: "Vendor L20",
					MemoryMiB:    81920,
					Healthy:      true,
				}},
			},
		},
		allocations: []v1.StaticNodeAllocationStatus{{
			WorkloadType: "endpoint",
			Endpoint:     "chat",
			ReplicaID:    "replica-0",
		}},
	}
	server, err := NewServer(Config{
		ClusterType:     "ray",
		AcceleratorType: "vendor",
		Accelerators: map[string]adapter.Accelerator{
			"vendor": accelerator,
		},
	})
	require.NoError(t, err)

	snapshot, err := server.nodeDeviceSnapshot(httptest.NewRequest(http.MethodGet, "/v1/node/device-snapshot", nil))
	require.NoError(t, err)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	require.Len(t, snapshot.Allocations, 1)
	assert.Equal(t, "vendor", snapshot.Accelerator.Type)
	assert.Equal(t, "vendor-0", snapshot.Accelerator.Devices[0].UUID)
	assert.Equal(t, "chat", snapshot.Allocations[0].Endpoint)
	assert.Equal(t, 1, accelerator.discoveries)
	assert.Equal(t, 1, accelerator.builds)
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
		ScrapeTargetProvider: testTargetProvider(
			nodeExporter.URL+"/metrics",
			acceleratorExporter.URL+"/metrics",
		),
		HTTPClient:          nodeExporter.Client(),
		GPUHardwareProvider: emptyGPUHardwareProvider,
		AllocationProvider: allocation.ProviderFunc(func(_ context.Context, snapshot *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
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
	assert.Contains(t, body, `neutree_node_accelerator_total{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_allocated{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, body, `neutree_node_accelerator_free{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 0`)
	allocationLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",endpoint="chat",instance_id="actor-a",node="head-0",product="NVIDIA_A100",replica="replica-a",vdevice_index="0"`
	allocationInfoLabels := `accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",endpoint="chat",instance_id="actor-a",node="head-0",physical_vram_usage="unknown",product="NVIDIA_A100",replica="replica-a",vdevice_index="0",vram_usage="4 GiB / 80 GiB"`
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_allocation{`+allocationInfoLabels+`} 1`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+allocationLabels+`}`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+allocationLabels+`}`)
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
		ScrapeTargetProvider: testTargetProvider(nodeExporter.URL + "/metrics"),
		HTTPClient:           nodeExporter.Client(),
		RuntimeUsageProvider: runtimeusage.ProviderFunc(func(_ context.Context) ([]model.EndpointReplicaRuntimeUsage, error) {
			return []model.EndpointReplicaRuntimeUsage{
				{
					Workspace:             "default",
					Cluster:               "static-a",
					Endpoint:              "chat",
					InstanceID:            "actor-a",
					ReplicaID:             "replica-a",
					NodeID:                "head-0",
					WorkloadRole:          model.WorkloadRoleBackend,
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
	runtimeLabels := `cluster_type="ray",container="engine",container_id="docker-abc",` +
		`endpoint="chat",engine="unknown",engine_version="unknown",instance_id="actor-a",` +
		`node="head-0",node_ip="10.0.0.10",node_role="head",replica="replica-a",` +
		`source="neutree-node-agent",workload_role="backend"`
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
		ScrapeTargetProvider: testTargetProvider(nodeExporter.URL + "/metrics"),
		HTTPClient:           nodeExporter.Client(),
		EndpointGPUUsageProvider: fakeEndpointGPUUsageProvider{
			usages: []model.EndpointReplicaGPUUsage{
				{
					Workspace:        "default",
					Cluster:          "k8s-a",
					Endpoint:         "chat",
					InstanceID:       "chat-abc",
					ReplicaID:        "chat-abc",
					NodeID:           "node-a",
					Container:        "engine",
					GPUUUID:          "GPU-abc",
					Product:          "NVIDIA_A100",
					MemoryUsedBytes:  &usedBytes,
					UtilizationRatio: &utilization,
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
		`cluster_type="kubernetes",endpoint="chat",instance_id="chat-abc",` +
		`node="node-a",product="NVIDIA_A100",replica="chat-abc",vdevice_index="0"`
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_memory_used_bytes{`+commonLabels+`}`)
	assert.Contains(t, body, `neutree_endpoint_replica_accelerator_utilization_ratio{`+commonLabels+`} 0.75`)
	assert.NotContains(t, body, `neutree_endpoint_replica_accelerator_allocation{`+commonLabels+`}`)
	assert.NotContains(t, body, `neutree_endpoint_replica_accelerator_memory_allocated_bytes{`+commonLabels+`}`)
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
		ScrapeTargetProvider: testTargetProvider(
			nodeExporter.URL+"/metrics",
			acceleratorExporter.URL+"/metrics",
		),
		HTTPClient:          nodeExporter.Client(),
		AllocationTimeout:   10 * time.Millisecond,
		GPUHardwareProvider: emptyGPUHardwareProvider,
		AllocationProvider: allocation.ProviderFunc(func(ctx context.Context, _ *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
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
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",node="node-a",product="A100"} 0.87`)
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
		ScrapeTargetProvider: testTargetProvider("", goodExporter.URL, badExporter.URL),
		HTTPClient:           goodExporter.Client(),
		GPUHardwareProvider:  emptyGPUHardwareProvider,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="neutree-node-agent",target="accelerator-exporter"} 1`)
	assert.Contains(t, body, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="kubernetes",node="node-a",product="A100"} 0.87`)
}

func TestServerSkipsAcceleratorHTTPSFallbackWhenHTTPAlreadySucceeded(t *testing.T) {
	httpsRequests := 0
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme == "https" {
				httpsRequests++
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`DCGM_FI_DEV_GPU_UTIL{gpu="1",UUID="GPU-def",modelName="A100"} 50`)),
					Header:     http.Header{},
					Request:    req,
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",modelName="A100"} 87`)),
				Header:     http.Header{},
				Request:    req,
			}, nil
		}),
	}

	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			ClusterType: "kubernetes",
			Node:        "node-a",
			NodeIP:      "10.0.0.10",
		},
		ScrapeTargetProvider: staticTestTargetProvider{
			metricsnormalizer.TargetAcceleratorExporter: {
				{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "http://exporter.local:9400/metrics"},
				{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "https://exporter.local:9400/metrics"},
			},
		},
		HTTPClient:          client,
		GPUHardwareProvider: emptyGPUHardwareProvider,
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })

	body := readResponseBody(t, metricsResp)
	assert.Equal(t, 0, httpsRequests)
	assert.Contains(t, body, `accelerator_uuid="GPU-abc"`)
	assert.NotContains(t, body, `accelerator_uuid="GPU-def"`)
}

func TestServerUsesNextNodeExporterTargetWhenFirstFails(t *testing.T) {
	badExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(badExporter.Close)

	goodExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`node_memory_MemTotal_bytes 17179869184
node_memory_MemAvailable_bytes 6442450944
`))
	}))
	t.Cleanup(goodExporter.Close)

	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		ScrapeTargetProvider: staticTestTargetProvider{
			metricsnormalizer.TargetNodeExporter: {
				{TargetType: metricsnormalizer.TargetNodeExporter, URL: badExporter.URL},
				{TargetType: metricsnormalizer.TargetNodeExporter, URL: goodExporter.URL},
			},
		},
		HTTPClient: goodExporter.Client(),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="neutree-node-agent",target="node-exporter"} 1`)
	assert.Contains(t, body, `neutree_node_memory_used_bytes{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="node-exporter"}`)
}

func TestServerReportsAcceleratorScrapeDownWhenProviderFindsNoTargets(t *testing.T) {
	server, err := NewServer(Config{
		Labels: model.CanonicalLabels{
			Workspace:      "default",
			NeutreeCluster: "k8s-a",
			ClusterType:    "kubernetes",
			Node:           "node-a",
			NodeIP:         "10.0.0.10",
		},
		ScrapeTargetProvider: staticTestTargetProvider{},
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	metricsResp, err := http.Get(httpServer.URL + "/metrics")
	require.NoError(t, err)
	t.Cleanup(func() { _ = metricsResp.Body.Close() })
	assert.Equal(t, http.StatusOK, metricsResp.StatusCode)

	body := readResponseBody(t, metricsResp)
	assert.Contains(t, body, `neutree_metrics_scrape_up{cluster_type="kubernetes",node="node-a",node_ip="10.0.0.10",node_role="unknown",source="neutree-node-agent",target="accelerator-exporter"} 0`)
}

func TestServerNodeDeviceSnapshotDoesNotBlockOnSlowAllocationProvider(t *testing.T) {
	server, err := NewServer(Config{
		AllocationTimeout: 10 * time.Millisecond,
		AllocationProvider: allocation.ProviderFunc(func(ctx context.Context, _ *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
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
		AllocationProvider: allocation.ProviderFunc(func(ctx context.Context, _ *v1.NodeDeviceSnapshot) ([]v1.StaticNodeAllocationStatus, error) {
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

func TestServerWriteKubernetesAnnotationsKeepsDeviceAnnotationOnEmptyCPUFallback(t *testing.T) {
	const devicesAnnotation = `[{"uuid":"GPU-abc","minor_number":0,"memory_mib":81920,"healthy":true}]`
	ctrClient := fake.NewClientBuilder().WithObjects(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-a",
			Annotations: map[string]string{
				accelerator.NeutreeAcceleratorDevicesAnnotation: devicesAnnotation,
			},
		},
	}).Build()
	server, err := NewServer(Config{
		ScrapeTargetProvider: staticTestTargetProvider{},
		KubernetesWriter: &metricskubernetes.AnnotationWriter{
			Client:   ctrClient,
			NodeName: "node-a",
		},
	})
	require.NoError(t, err)

	server.writeKubernetesAnnotations(context.Background())

	node := &corev1.Node{}
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node))
	assert.Equal(t, devicesAnnotation, node.Annotations[accelerator.NeutreeAcceleratorDevicesAnnotation])
}

func TestServerWriteKubernetesAnnotationsClearsDeviceAnnotationOnEmptyExplicitAdapter(t *testing.T) {
	const devicesAnnotation = `[{"uuid":"GPU-abc","minor_number":0,"memory_mib":81920,"healthy":true}]`
	testAccelerator := &recordingKubernetesAccelerator{
		capabilityTestAccelerator: capabilityTestAccelerator{typ: "vendor"},
	}
	ctrClient := fake.NewClientBuilder().
		WithObjects(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-a",
				Annotations: map[string]string{
					accelerator.NeutreeAcceleratorDevicesAnnotation: devicesAnnotation,
				},
			},
		}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
			pod, ok := object.(*corev1.Pod)
			if !ok || pod.Spec.NodeName == "" {
				return nil
			}

			return []string{pod.Spec.NodeName}
		}).
		Build()
	server, err := NewServer(Config{
		ClusterType:     "kubernetes",
		AcceleratorType: "vendor",
		Accelerators: map[string]adapter.Accelerator{
			"vendor": testAccelerator,
		},
		KubernetesWriter: &metricskubernetes.AnnotationWriter{
			Client:   ctrClient,
			NodeName: "node-a",
		},
	})
	require.NoError(t, err)

	server.writeKubernetesAnnotations(context.Background())

	node := &corev1.Node{}
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node))
	assert.JSONEq(t, `[]`, node.Annotations[accelerator.NeutreeAcceleratorDevicesAnnotation])
}

func TestServerNodeDeviceSnapshotSetsMinorNumberFromHardwareInfo(t *testing.T) {
	acceleratorExporter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`DCGM_FI_DEV_GPU_UTIL{gpu="7",UUID="GPU-abc",modelName="A100"} 87`))
	}))
	t.Cleanup(acceleratorExporter.Close)

	server, err := NewServer(Config{
		ScrapeTargetProvider: testTargetProvider("", acceleratorExporter.URL),
		HTTPClient:           acceleratorExporter.Client(),
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

	var snapshot v1.NodeDeviceSnapshot
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&snapshot))
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "7", snapshot.Accelerator.Devices[0].ID)
	require.NotNil(t, snapshot.Accelerator.Devices[0].MinorNumber)
	assert.Equal(t, 3, *snapshot.Accelerator.Devices[0].MinorNumber)
}

func TestServerNodeDeviceSnapshotAllowsRequests(t *testing.T) {
	server, err := NewServer(Config{
		DeviceSnapshotProvider: model.DeviceSnapshotProviderFunc(func(_ *http.Request) (*v1.NodeDeviceSnapshot, error) {
			cpu := v1.CPUStaticNodeAcceleratorStatus()

			return &v1.NodeDeviceSnapshot{Accelerator: cpu}, nil
		}),
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)

	resp, err := http.Get(httpServer.URL + "/v1/node/device-snapshot")
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var snapshot v1.NodeDeviceSnapshot
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

type staticTestTargetProvider map[string][]ScrapeTarget

func (p staticTestTargetProvider) Targets(_ context.Context, targetType string) ([]ScrapeTarget, error) {
	return p[targetType], nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testTargetProvider(nodeURL string, acceleratorURLs ...string) staticTestTargetProvider {
	provider := staticTestTargetProvider{}
	if nodeURL != "" {
		provider[metricsnormalizer.TargetNodeExporter] = []ScrapeTarget{
			{TargetType: metricsnormalizer.TargetNodeExporter, URL: nodeURL},
		}
	}
	for _, acceleratorURL := range acceleratorURLs {
		if acceleratorURL == "" {
			continue
		}
		provider[metricsnormalizer.TargetAcceleratorExporter] = append(
			provider[metricsnormalizer.TargetAcceleratorExporter],
			ScrapeTarget{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: acceleratorURL},
		)
	}

	return provider
}
