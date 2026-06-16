package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var (
	testNvidiaLspciOutput = `00:00.0 Host bridge [0600]: Intel Corporation 82G33/G31/P35/P31 Express DRAM Controller [8086:29c0]
			00:01.0 VGA compatible controller [0300]: Red Hat, Inc. Virtio GPU [1af4:1050] (rev 01)
			00:02.0 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			3b:00.0 3D controller [0302]: NVIDIA Corporation A100 80GB PCIe [10de:20b2] (rev a1)
			3b:00.1 Audio device [0403]: NVIDIA Corporation GA100 High Definition Audio Controller [10de:1aef] (rev a1)
			86:00.0 3D controller [0302]: NVIDIA Corporation A100 80GB PCIe [10de:20b2] (rev a1)
			86:00.1 Audio device [0403]: NVIDIA Corporation GA100 High Definition Audio Controller [10de:1aef] (rev a1)
			01:00.0 Ethernet controller [0200]: Red Hat, Inc. Virtio network device [1af4:1041] (rev 01)`

	testNvidiaVGALspciOutput = `00:00.0 Host bridge [0600]: Intel Corporation Device [8086:4660] (rev 02)
			01:00.0 VGA compatible controller [0300]: NVIDIA Corporation GP107 [GeForce GTX 1050 Ti] [10de:1c82] (rev a1)
			01:00.1 Audio device [0403]: NVIDIA Corporation GP107GL High Definition Audio Controller [10de:0fb9] (rev a1)`
)

type staticNodeTestRunner struct {
	output string
	err    error
	calls  int
}

func (r *staticNodeTestRunner) Run(ctx context.Context, command string) (string, error) {
	r.calls++

	return r.output, r.err
}

func TestGPUAcceleratorPlugin_BasicMethods(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	// Test basic interface methods
	assert.Equal(t, string(v1.AcceleratorTypeNVIDIAGPU), plugin.Resource())
	assert.Equal(t, plugin, plugin.Handle())
	assert.Equal(t, InternalPluginType, plugin.Type())
	assert.NoError(t, plugin.Ping(context.Background()))
}

func TestGPUAcceleratorPluginDetectStaticNodeAccelerator(t *testing.T) {
	runner := &staticNodeTestRunner{output: testNvidiaLspciOutput}
	p := &GPUAcceleratorPlugin{}

	status, matched, err := p.DetectStaticNodeAccelerator(context.Background(), runner)

	require.NoError(t, err)
	require.True(t, matched)
	require.NotNil(t, status)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), status.Type)
	assert.Equal(t, "nvidia", status.Vendor)
	assert.Equal(t, "NVIDIA GPU", status.ProductName)
	assert.Equal(t, "nvidia_gpu", status.ProductModel)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), status.RuntimeProfile)
	assert.Equal(t, "GPU", status.ResourceName)
	require.Len(t, status.Devices, 2)
	assert.Equal(t, "0", status.Devices[0].ID)
	assert.True(t, status.Devices[0].Healthy)
	assert.Equal(t, 1, runner.calls)
}

func TestGPUAcceleratorPluginDetectStaticNodeAcceleratorNoMatch(t *testing.T) {
	runner := &staticNodeTestRunner{output: "{}"}
	p := &GPUAcceleratorPlugin{}

	status, matched, err := p.DetectStaticNodeAccelerator(context.Background(), runner)

	require.NoError(t, err)
	assert.False(t, matched)
	assert.Nil(t, status)
}

func TestGPUAcceleratorPluginDetectStaticNodeAcceleratorReturnsRunnerError(t *testing.T) {
	runner := &staticNodeTestRunner{err: errors.New("lspci failed")}
	p := &GPUAcceleratorPlugin{}

	status, matched, err := p.DetectStaticNodeAccelerator(context.Background(), runner)

	require.Error(t, err)
	assert.False(t, matched)
	assert.Nil(t, status)
}

func TestGPUAcceleratorPluginRuntimeProfile(t *testing.T) {
	p := &GPUAcceleratorPlugin{}

	profile, supported, err := p.RuntimeProfile(context.Background(), v1.StaticNodeAcceleratorStatus{
		Type:           v1.AcceleratorTypeNVIDIAGPU.String(),
		RuntimeProfile: "nvidia-a100",
	})

	require.NoError(t, err)
	assert.True(t, supported)
	require.NotNil(t, profile)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), profile.AcceleratorType)
}

func TestGPUAcceleratorPlugin_GetNodeAcceleratorInfo(t *testing.T) {
	tests := []struct {
		name                     string
		mockSetup                func(*commandmocks.MockExecutor)
		expectedAcceleratorCount int
	}{
		{
			name: "Node without GPU resources",
			mockSetup: func(mockExector *commandmocks.MockExecutor) {
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "uptime"))
				}).Return([]byte("{}"), nil).Once()
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "lspci -nn"))
				}).Return([]byte("{}"), nil).Once()
			},
			expectedAcceleratorCount: 0,
		},
		{
			name: "Node with 2 NVIDIA 3D controller GPUs (should not count audio devices)",
			mockSetup: func(mockExector *commandmocks.MockExecutor) {
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "uptime"))
				}).Return([]byte("{}"), nil).Once()
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "lspci -nn"))
				}).Return([]byte(testNvidiaLspciOutput), nil).Once()
			},
			expectedAcceleratorCount: 2,
		},
		{
			name: "Node with 1 NVIDIA VGA compatible GPU",
			mockSetup: func(mockExector *commandmocks.MockExecutor) {
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "uptime"))
				}).Return([]byte("{}"), nil).Once()
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "lspci -nn"))
				}).Return([]byte(testNvidiaVGALspciOutput), nil).Once()
			},
			expectedAcceleratorCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExector := &commandmocks.MockExecutor{}
			tt.mockSetup(mockExector)

			p := &GPUAcceleratorPlugin{
				executor: mockExector,
			}
			accelerators, err := p.getNodeAcceleratorInfo(context.Background(), "127.0.0.1", v1.Auth{
				SSHUser:       "root",
				SSHPrivateKey: "MTIzCg==",
			})
			assert.NoError(t, err)
			assert.Len(t, accelerators, tt.expectedAcceleratorCount)
		})
	}
}

func TestGPUAcceleratorPlugin_GetNodeRuntimeConfig(t *testing.T) {
	tests := []struct {
		name                string
		mockSetup           func(*commandmocks.MockExecutor)
		expectRuntimeConfig v1.RuntimeConfig
	}{
		{
			name: "Node without GPU resources",
			mockSetup: func(mockExector *commandmocks.MockExecutor) {
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "uptime"))
				}).Return([]byte("{}"), nil).Once()
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "lspci -nn"))
				}).Return([]byte("{}"), nil).Once()
			},
			expectRuntimeConfig: v1.RuntimeConfig{},
		},
		{
			name: "Node with GPU resources",
			mockSetup: func(mockExector *commandmocks.MockExecutor) {
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "uptime"))
				}).Return([]byte("{}"), nil).Once()
				mockExector.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					cmds := args.Get(2).([]string)
					assert.True(t, strings.Contains(strings.Join(cmds, " "), "lspci -nn"))
				}).Return([]byte(testNvidiaLspciOutput), nil).Once()
			},
			expectRuntimeConfig: v1.RuntimeConfig{
				Runtime: "nvidia",
				Env: map[string]string{
					"ACCELERATOR_TYPE": "gpu",
				},
				Options: []string{"--gpus all"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExector := &commandmocks.MockExecutor{}
			tt.mockSetup(mockExector)

			p := &GPUAcceleratorPlugin{
				executor: mockExector,
			}
			runtimeConfig, err := p.GetNodeRuntimeConfig(context.Background(), &v1.GetNodeRuntimeConfigRequest{
				NodeIp: "127.0.0.1",
				SSHAuth: v1.Auth{
					SSHUser:       "root",
					SSHPrivateKey: "MTIzCg==",
				},
			})
			assert.NoError(t, err)
			assert.NotNil(t, runtimeConfig)
			assert.Equal(t, tt.expectRuntimeConfig, runtimeConfig.RuntimeConfig)
		})
	}
}

func TestGPUAcceleratorPlugin_GetAcceleratorProfile(t *testing.T) {
	p := &GPUAcceleratorPlugin{}

	profile, err := p.GetAcceleratorProfile(context.Background())

	require.NoError(t, err)
	require.NotNil(t, profile)
	require.NotNil(t, profile.ClusterRuntime)
	require.NotNil(t, profile.EndpointRuntime)
	require.NotNil(t, profile.ResourceDefaults)
	require.NotNil(t, profile.Metrics)
	require.NotNil(t, profile.Metrics.Exporter)
	assert.Equal(t, string(v1.AcceleratorTypeNVIDIAGPU), profile.AcceleratorType)
	assert.Equal(t, "nvidia", profile.ClusterRuntime.Runtime)
	assert.Equal(t, []string{"--gpus all"}, profile.EndpointRuntime.Options)
	assert.Equal(t, "GPU", profile.ResourceDefaults.RayResourceName)
	assert.Equal(t, string(NvidiaGPUKubernetesResource), profile.ResourceDefaults.KubernetesResourceName)
	assert.Equal(t, "dcgm-exporter", profile.Metrics.Exporter.Kind)
	assert.Equal(t, v1.NodeComponentTypeAcceleratorExporter, profile.Metrics.Exporter.ComponentType)
	assert.Equal(t, nvidiaDCGMExporterImage, profile.Metrics.Exporter.Image)
	assert.Equal(t, nvidiaDCGMExporterPort, profile.Metrics.Exporter.Port)
	assert.Equal(t, []string{"--collectors", nvidiaDCGMExporterCollectorsPath}, profile.Metrics.Exporter.Args)
	require.Len(t, profile.Metrics.Exporter.ConfigFiles, 1)
	assert.Equal(t, nvidiaDCGMExporterCollectorsPath, profile.Metrics.Exporter.ConfigFiles[0].Path)
	assert.NotContains(t, profile.Metrics.Exporter.ConfigFiles[0].Content, "PROF")
	require.Len(t, profile.Metrics.Exporter.Volumes, 1)
	assert.Equal(t, nvidiaDCGMExporterCollectorsPath, profile.Metrics.Exporter.Volumes[0].MountPath)
}
