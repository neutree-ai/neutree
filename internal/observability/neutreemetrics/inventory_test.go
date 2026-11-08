package neutreemetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestServerInventoryRetainsMissingDevicesAndRecovers(t *testing.T) {
	accel := &inventoryTestAccelerator{hardware: inventoryTestHardware(8)}
	server := newInventoryTestServer(t, accel)
	initial := server.nodeDeviceSnapshot(nil)
	require.Len(t, initial.Accelerator.Devices, 8)

	short := inventoryTestHardware(8)
	short.Accelerator.Devices = append(short.Accelerator.Devices[:4], short.Accelerator.Devices[5:]...)
	short.Details = append(short.Details[:4], short.Details[5:]...)
	accel.setHardware(short, nil)
	for range 2 {
		snapshot := server.nodeDeviceSnapshot(nil)
		require.Len(t, snapshot.Accelerator.Devices, 8)
		for i, device := range snapshot.Accelerator.Devices {
			assert.Equal(t, fmt.Sprintf("GPU-%d", i), device.UUID)
			assert.Equal(t, fmt.Sprint(i), device.ID)
			require.NotNil(t, device.MinorNumber)
			assert.Equal(t, i, *device.MinorNumber)
			assert.Equal(t, i != 4, device.Healthy)
		}
	}

	accel.setHardware(inventoryTestHardware(8), nil)
	recovered := server.nodeDeviceSnapshot(nil)
	require.Len(t, recovered.Accelerator.Devices, 8)
	for _, device := range recovered.Accelerator.Devices {
		assert.True(t, device.Healthy)
	}
}

func TestServerInventoryRetainsIdentityOnPartialRediscovery(t *testing.T) {
	accel := &inventoryTestAccelerator{hardware: inventoryTestHardware(1)}
	server := newInventoryTestServer(t, accel)
	server.nodeDeviceSnapshot(nil)
	partial := inventoryTestHardware(1)
	partial.Accelerator.Devices[0] = v1.StaticNodeAcceleratorDeviceStatus{ID: "0", UUID: "GPU-0", Healthy: false}
	partial.Details = nil
	accel.setHardware(partial, nil)
	snapshot := server.nodeDeviceSnapshot(nil)
	require.Len(t, snapshot.Accelerator.Devices, 1)
	assert.Equal(t, "0", snapshot.Accelerator.Devices[0].ID)
	require.NotNil(t, snapshot.Accelerator.Devices[0].MinorNumber)
	assert.Equal(t, 0, *snapshot.Accelerator.Devices[0].MinorNumber)
	assert.Equal(t, "RTX 5090", snapshot.Accelerator.Devices[0].ProductName)
	assert.Equal(t, int64(32768), snapshot.Accelerator.Devices[0].MemoryMiB)
	assert.False(t, snapshot.Accelerator.Devices[0].Healthy)
}

func TestServerInventoryRetainsCacheAndAllocationsOnDiscoveryFailure(t *testing.T) {
	accel := &inventoryTestAccelerator{
		hardware:    inventoryTestHardware(8),
		allocations: []v1.StaticNodeAllocationStatus{{ReplicaID: "ns/pod-a"}},
	}
	server := newInventoryTestServer(t, accel)
	server.nodeDeviceSnapshot(nil)
	accel.setHardware(adapter.HardwareSnapshot{}, fmt.Errorf("nvmlDeviceGetCount failed"))

	snapshot := server.nodeDeviceSnapshot(nil)

	require.Len(t, snapshot.Accelerator.Devices, 8)
	for _, device := range snapshot.Accelerator.Devices {
		assert.False(t, device.Healthy)
	}
	require.Len(t, snapshot.Allocations, 1)
	assert.Equal(t, "ns/pod-a", snapshot.Allocations[0].ReplicaID)
}

func TestServerInventoryHealthDoesNotReviveLostGPU(t *testing.T) {
	accel := &inventoryTestHealthAccelerator{inventoryTestAccelerator: &inventoryTestAccelerator{hardware: inventoryTestHardware(8)}}
	server := newInventoryTestServer(t, accel)
	pass := &model.ScrapeResult{Up: true}
	server.acceleratorSamples(context.Background(), accel, pass)

	short := inventoryTestHardware(8)
	short.Accelerator.Devices = append(short.Accelerator.Devices[:4], short.Accelerator.Devices[5:]...)
	accel.setHardware(short, nil)
	hardware, err := server.observeAdapterHardware(context.Background(), accel, pass)
	require.NoError(t, err)
	require.Len(t, hardware.Accelerator.Devices, 8)
	assert.False(t, hardware.Accelerator.Devices[4].Healthy, "exporter PASS cannot revive an undiscovered UUID")

	accel.setHardware(inventoryTestHardware(8), nil)
	hardware, err = server.observeAdapterHardware(context.Background(), accel, nil)
	require.NoError(t, err)
	assert.True(t, hardware.Accelerator.Devices[4].Healthy, "a successful fresh discovery clears the historical failure")

	hardware, err = server.observeAdapterHardware(context.Background(), accel, pass)
	require.NoError(t, err)
	assert.True(t, hardware.Accelerator.Devices[4].Healthy)
}

func TestServerInventoryAnnotationUsesLatestMergedCache(t *testing.T) {
	accel := &inventoryTestAccelerator{
		hardware: inventoryTestHardware(8), buildEntered: make(chan struct{}), buildRelease: make(chan struct{}),
	}
	server := newInventoryTestServer(t, accel)
	ctrClient := fake.NewClientBuilder().WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string { return nil }).Build()
	server.config.KubernetesWriter = &metricskubernetes.AnnotationWriter{Client: ctrClient, NodeName: "node-a"}
	done := make(chan struct{})
	go func() {
		server.writeKubernetesAnnotations(context.Background())
		close(done)
	}()
	<-accel.buildEntered
	accel.setHardware(inventoryTestHardware(7), nil)
	server.acceleratorSamples(context.Background(), accel, nil)
	close(accel.buildRelease)
	<-done
	node := &corev1.Node{}
	require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node))
	var devices []v1.StaticNodeAcceleratorDeviceStatus
	require.NoError(t, json.Unmarshal([]byte(node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]), &devices))
	require.Len(t, devices, 8)
	assert.Equal(t, "GPU-7", devices[7].UUID)
	assert.False(t, devices[7].Healthy, "an older annotation snapshot must not overwrite a newer health observation")
}

func newInventoryTestServer(t *testing.T, accel adapter.Accelerator) *Server {
	t.Helper()
	server, err := NewServer(Config{
		ClusterType: v1.KubernetesClusterType, AcceleratorType: "vendor", Accelerators: map[string]adapter.Accelerator{"vendor": accel},
	})
	require.NoError(t, err)
	return server
}

func inventoryTestHardware(count int) adapter.HardwareSnapshot {
	result := adapter.HardwareSnapshot{Accelerator: v1.StaticNodeAcceleratorStatus{Type: "vendor"}}
	for i := range count {
		minor := i
		result.Accelerator.Devices = append(result.Accelerator.Devices, v1.StaticNodeAcceleratorDeviceStatus{
			UUID: fmt.Sprintf("GPU-%d", i), ID: fmt.Sprint(i), MinorNumber: &minor, Healthy: true,
			ProductName: "RTX 5090", ProductModel: "RTX-5090", MemoryMiB: 32768,
		})
		result.Details = append(result.Details, adapter.HardwareDetails{UUID: fmt.Sprintf("GPU-%d", i), PCIEBusID: fmt.Sprint(i)})
	}
	return result
}

type inventoryTestAccelerator struct {
	mu           sync.Mutex
	hardware     adapter.HardwareSnapshot
	discoveryErr error
	builds       int
	buildEntered chan struct{}
	buildRelease chan struct{}
	allocations  []v1.StaticNodeAllocationStatus
}

func (*inventoryTestAccelerator) Type() string { return "vendor" }

func (a *inventoryTestAccelerator) setHardware(hardware adapter.HardwareSnapshot, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hardware = hardware.Clone()
	a.discoveryErr = err
}

func (a *inventoryTestAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hardware.Clone(), a.discoveryErr
}

func (a *inventoryTestAccelerator) BuildKubernetesMetrics(
	_ context.Context, _ adapter.HardwareSnapshot, _ adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	a.mu.Lock()
	a.builds++
	first := a.builds == 1
	a.mu.Unlock()
	if first && a.buildEntered != nil {
		close(a.buildEntered)
		<-a.buildRelease
	}
	return adapter.MetricResult{Allocations: a.allocations}, nil
}

type inventoryTestHealthAccelerator struct{ *inventoryTestAccelerator }

func (*inventoryTestHealthAccelerator) EvaluateHardwareHealth(hardware adapter.HardwareSnapshot, evidence adapter.CommonEvidence) adapter.HardwareSnapshot {
	result := hardware.Clone()
	if evidence.ExporterUp {
		for i := range result.Accelerator.Devices {
			result.Accelerator.Devices[i].Healthy = true
		}
	}
	return result
}

func TestServerInventorySharesCacheAcrossConcurrentHTTPRequests(t *testing.T) {
	accel := &inventoryTestAccelerator{hardware: inventoryTestHardware(8)}
	server := newInventoryTestServer(t, accel)
	baseline := httptest.NewRecorder()
	server.Handler().ServeHTTP(baseline, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, baseline.Code)
	accel.setHardware(inventoryTestHardware(7), nil)

	metrics := httptest.NewRecorder()
	response := httptest.NewRecorder()
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		server.Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	}()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/node/device-snapshot", nil))
	group.Wait()

	require.Equal(t, http.StatusOK, metrics.Code)
	require.Equal(t, http.StatusOK, response.Code)
	var snapshot v1.NodeDeviceSnapshot
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &snapshot))
	require.Len(t, snapshot.Accelerator.Devices, 8)
	assert.Equal(t, "GPU-7", snapshot.Accelerator.Devices[7].UUID)
	assert.False(t, snapshot.Accelerator.Devices[7].Healthy)
}

func TestServerInventoryUsesLocalExporterPresenceOnMixedNodes(t *testing.T) {
	labels := map[string]string{
		AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
		AcceleratorExporterTypeLabel:   "vendor",
	}
	for _, hasLocalExporter := range []bool{false, true} {
		t.Run(fmt.Sprintf("local_exporter=%t", hasLocalExporter), func(t *testing.T) {
			pods := []*corev1.Pod{pod("metrics", "other-exporter", "node-b", "", labels)}
			if hasLocalExporter {
				pods = append(pods, pod("metrics", "local-exporter", "node-a", "", labels))
			}
			provider := newKubernetesTargetProvider(t, pods...)
			provider.NodeName = "node-a"
			provider.AcceleratorType = "vendor"
			accel := &inventoryTestAccelerator{hardware: inventoryTestHardware(8)}
			server := newInventoryTestServer(t, accel)
			server.config.ScrapeTargetProvider = provider

			snapshot := server.nodeDeviceSnapshot(nil)

			if hasLocalExporter {
				assert.Equal(t, "vendor", snapshot.Accelerator.Type)
				assert.Len(t, snapshot.Accelerator.Devices, 8)
			} else {
				assert.Equal(t, v1.StaticNodeAcceleratorTypeCPU, snapshot.Accelerator.Type)
				assert.Empty(t, snapshot.Accelerator.Devices)
			}
		})
	}
}

func TestServerInventoryReportsExporterDiscoveryFailure(t *testing.T) {
	listErr := fmt.Errorf("pods is forbidden")
	provider := newKubernetesTargetProvider(t)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "vendor"
	provider.Client = exporterPodListErrorClient{Client: provider.Client, err: listErr}
	accel := &inventoryTestAccelerator{hardware: inventoryTestHardware(8)}
	server := newInventoryTestServer(t, accel)
	server.config.ScrapeTargetProvider = provider

	_, err := server.observeAdapterHardware(context.Background(), accel, nil)

	assert.ErrorIs(t, err, listErr)
}

type exporterPodListErrorClient struct {
	client.Client
	err error
}

func (c exporterPodListErrorClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}
