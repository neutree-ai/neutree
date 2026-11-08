package neutreemetrics

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestServerStartupValidationOnMixedKubernetesNodes(t *testing.T) {
	initErr := errors.New("nvmlInit: ERROR_DRIVER_NOT_LOADED (ret=9)")
	exporterLabels := map[string]string{
		AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
		AcceleratorExporterTypeLabel:   "nvidia_gpu",
	}
	cases := []struct {
		name          string
		localExporter *corev1.Pod
		validationErr error
		wantCalls     int
	}{
		{
			name:          "CPU node with exporter only on another node",
			validationErr: initErr,
		},
		{
			name:          "GPU exporter scheduled without Pod IP",
			localExporter: pod("metrics", "dcgm-local", "node-a", "", exporterLabels),
			validationErr: initErr,
			wantCalls:     1,
		},
		{
			name:          "healthy GPU node",
			localExporter: pod("metrics", "dcgm-local", "node-a", "10.244.0.30", exporterLabels),
			wantCalls:     1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pods := []*corev1.Pod{
				pod("metrics", "dcgm-other", "node-b", "10.244.0.31", exporterLabels),
				pod("metrics", "node-exporter", "node-a", "10.244.0.20", map[string]string{"app": managedNodeExporterApp}),
			}
			if tc.localExporter != nil {
				pods = append(pods, tc.localExporter)
			}
			provider := newKubernetesTargetProvider(t, pods...)
			provider.NodeName = "node-a"
			provider.AcceleratorType = "nvidia_gpu"
			accel := &startupValidationTestAccelerator{err: tc.validationErr}
			server, err := NewServer(Config{
				ClusterType:          v1.KubernetesClusterType,
				AcceleratorType:      accel.Type(),
				ScrapeTargetProvider: provider,
			}.WithAccelerators(map[string]adapter.Accelerator{accel.Type(): accel}))
			require.NoError(t, err)

			err = server.validateAcceleratorStartup(context.Background())

			assert.Equal(t, tc.wantCalls, accel.calls)
			if tc.wantCalls > 0 && tc.validationErr != nil {
				assert.ErrorIs(t, err, tc.validationErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestServerStartupValidationForStaticGPUNode(t *testing.T) {
	initErr := errors.New("nvmlInit: ERROR_DRIVER_NOT_LOADED (ret=9)")
	accel := &startupValidationTestAccelerator{err: initErr}
	server, err := NewServer(Config{
		ClusterType:          v1.SSHClusterType,
		AcceleratorType:      accel.Type(),
		ScrapeTargetProvider: StaticScrapeTargetProvider{AcceleratorType: accel.Type()},
	}.WithAccelerators(map[string]adapter.Accelerator{accel.Type(): accel}))
	require.NoError(t, err)

	err = server.Run(context.Background())

	assert.ErrorIs(t, err, initErr)
	assert.Equal(t, 1, accel.calls)
}

func TestServerStartupValidationReportsExporterPodListFailure(t *testing.T) {
	listErr := errors.New("pods is forbidden")
	provider := newKubernetesTargetProvider(t)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "nvidia_gpu"
	provider.Client = exporterPodListErrorClient{Client: provider.Client, err: listErr}
	accel := &startupValidationTestAccelerator{}
	server, err := NewServer(Config{
		ClusterType:          v1.KubernetesClusterType,
		AcceleratorType:      accel.Type(),
		ScrapeTargetProvider: provider,
	}.WithAccelerators(map[string]adapter.Accelerator{accel.Type(): accel}))
	require.NoError(t, err)

	err = server.Run(context.Background())

	assert.ErrorIs(t, err, listErr)
	assert.ErrorContains(t, err, "list accelerator exporter pods")
	assert.Zero(t, accel.calls)
}

type exporterPodListErrorClient struct {
	client.Client
	err error
}

func (c exporterPodListErrorClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return c.err
}

type startupValidationTestAccelerator struct {
	failingAccelerator
	calls int
	err   error
}

func (a *startupValidationTestAccelerator) ValidateHardware(context.Context) error {
	a.calls++
	return a.err
}
