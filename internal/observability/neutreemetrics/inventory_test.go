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

func TestServerInventoryLossAndRecovery(t *testing.T) {
	healthy := inventoryTestHardware(8)
	unhealthy := inventoryTestHardware(8)
	for i := range unhealthy.Accelerator.Devices {
		unhealthy.Accelerator.Devices[i].Healthy = false
	}
	short := inventoryTestHardware(8)
	short.Accelerator.Devices = append(short.Accelerator.Devices[:4], short.Accelerator.Devices[5:]...)
	short.Details = append(short.Details[:4], short.Details[5:]...)
	partial := inventoryTestHardware(8)
	partial.Accelerator.Devices[4] = v1.StaticNodeAcceleratorDeviceStatus{ID: "4", UUID: "GPU-4", Healthy: false}
	partial.Details = nil
	cases := []struct {
		name          string
		initial       adapter.HardwareSnapshot
		hardware      adapter.HardwareSnapshot
		err           error
		exporter      *model.ScrapeResult
		wantUnhealthy []int
	}{
		{
			name: "GPU 4 disappears while exporter still reports PASS", initial: healthy, hardware: short,
			exporter: &model.ScrapeResult{Up: true}, wantUnhealthy: []int{4},
		},
		{name: "GPU 4 identity survives partial rediscovery", initial: healthy, hardware: partial, wantUnhealthy: []int{4}},
		{
			name: "device count query fails", initial: healthy, err: fmt.Errorf("nvmlDeviceGetCount failed"),
			wantUnhealthy: []int{0, 1, 2, 3, 4, 5, 6, 7},
		},
		{name: "successful rediscovery restores health without exporter evidence", initial: unhealthy, hardware: healthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allocations := []v1.StaticNodeAllocationStatus{{
				ReplicaID: "ns/pod-a", Devices: []v1.DeviceAllocation{{UUID: "GPU-7", MemoryMiB: 30720, CoreUnits: 85}},
			}}
			accel := &inventoryTestHealthAccelerator{inventoryTestAccelerator: &inventoryTestAccelerator{
				hardware: tc.initial, allocations: allocations,
			}}
			server := newInventoryTestServer(t, accel)
			server.nodeDeviceSnapshot(nil)
			accel.setHardware(tc.hardware, tc.err)
			hardware, err := server.observeAdapterHardware(context.Background(), accel, tc.exporter)
			require.ErrorIs(t, err, tc.err)
			want := inventoryTestHardware(8)
			for _, index := range tc.wantUnhealthy {
				want.Accelerator.Devices[index].Healthy = false
			}
			assert.Equal(t, want, hardware)

			snapshot := server.nodeDeviceSnapshot(nil)
			assert.Equal(t, want.Accelerator, snapshot.Accelerator)
			assert.Equal(t, allocations, snapshot.Allocations)
		})
	}
}

func TestServerInventoryConcurrentConsumers(t *testing.T) {
	cases := []struct {
		name        string
		annotations bool
	}{
		{name: "metrics and HTTP snapshots share inventory"},
		{name: "annotations publish the latest inventory", annotations: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accel := &inventoryTestAccelerator{
				hardware: inventoryTestHardware(8), buildEntered: make(chan struct{}), buildRelease: make(chan struct{}),
			}
			server := newInventoryTestServer(t, accel)
			ctrClient := fake.NewClientBuilder().WithObjects(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}).
				WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string { return nil }).Build()
			server.config.KubernetesWriter = &metricskubernetes.AnnotationWriter{Client: ctrClient, NodeName: "node-a"}
			baseline := httptest.NewRecorder()
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				if tc.annotations {
					server.writeKubernetesAnnotations(context.Background())
				} else {
					server.Handler().ServeHTTP(baseline, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				}
				close(done)
			}()
			<-accel.buildEntered
			accel.setHardware(inventoryTestHardware(7), nil)
			if tc.annotations {
				server.acceleratorSamples(context.Background(), accel, nil)
			} else {
				server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/node/device-snapshot", nil))
			}
			close(accel.buildRelease)
			<-done

			var devices []v1.StaticNodeAcceleratorDeviceStatus
			if tc.annotations {
				node := &corev1.Node{}
				require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: "node-a"}, node))
				require.NoError(t, json.Unmarshal([]byte(node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]), &devices))
			} else {
				require.Equal(t, http.StatusOK, baseline.Code)
				require.Equal(t, http.StatusOK, response.Code)
				var snapshot v1.NodeDeviceSnapshot
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &snapshot))
				devices = snapshot.Accelerator.Devices
			}
			require.Len(t, devices, 8)
			assert.Equal(t, "GPU-7", devices[7].UUID)
			assert.False(t, devices[7].Healthy)
		})
	}
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

func TestServerInventoryOnMixedNodes(t *testing.T) {
	labels := map[string]string{
		AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
		AcceleratorExporterTypeLabel:   "vendor",
	}
	listErr := fmt.Errorf("pods is forbidden")
	cases := []struct {
		name        string
		localPod    *corev1.Pod
		listErr     error
		wantType    string
		wantDevices int
	}{
		{name: "CPU node with exporter only on another node", wantType: v1.StaticNodeAcceleratorTypeCPU},
		{
			name: "GPU exporter scheduled without Pod IP", localPod: pod("metrics", "local-exporter", "node-a", "", labels),
			wantType: "vendor", wantDevices: 8,
		},
		{name: "exporter Pod query fails", listErr: listErr, wantType: "vendor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pods := []*corev1.Pod{pod("metrics", "other-exporter", "node-b", "", labels)}
			if tc.localPod != nil {
				pods = append(pods, tc.localPod)
			}
			provider := newKubernetesTargetProvider(t, pods...)
			provider.NodeName = "node-a"
			provider.AcceleratorType = "vendor"
			if tc.listErr != nil {
				provider.Client = exporterPodListErrorClient{Client: provider.Client, err: tc.listErr}
			}
			accel := &inventoryTestAccelerator{hardware: inventoryTestHardware(8)}
			server := newInventoryTestServer(t, accel)
			server.config.ScrapeTargetProvider = provider

			hardware, err := server.observeAdapterHardware(context.Background(), accel, nil)

			require.ErrorIs(t, err, tc.listErr)
			assert.Equal(t, tc.wantType, hardware.Accelerator.Type)
			assert.Len(t, hardware.Accelerator.Devices, tc.wantDevices)
		})
	}
}

type exporterPodListErrorClient struct {
	client.Client
	err error
}

func (c exporterPodListErrorClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}
