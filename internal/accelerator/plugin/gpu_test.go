package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/neutree-ai/neutree/pkg/command_runner"
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

func TestGPUAcceleratorPlugin_BasicMethods(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	// Test basic interface methods
	assert.Equal(t, string(v1.AcceleratorTypeNVIDIAGPU), plugin.Resource())
	assert.Equal(t, plugin, plugin.Handle())
	assert.Equal(t, InternalPluginType, plugin.Type())
	assert.NoError(t, plugin.Ping(context.Background()))
}

func TestGPUAcceleratorPluginDetectStaticNodeAccelerator(t *testing.T) {
	mockExecutor := &commandmocks.MockExecutor{}
	mockNodeAcceleratorCommands(t, mockExecutor, testNvidiaLspciOutput, nil)
	p := &GPUAcceleratorPlugin{executor: mockExecutor}

	response, err := p.DetectStaticNodeAccelerator(context.Background(), staticNodeAcceleratorTestRequest())

	require.NoError(t, err)
	require.NotNil(t, response)
	require.True(t, response.Matched)
	status := response.Accelerator
	require.NotNil(t, status)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), status.Type)
	assert.Empty(t, status.Devices)
	mockExecutor.AssertExpectations(t)
}

func TestGPUAcceleratorPluginDetectStaticNodeAcceleratorNoMatch(t *testing.T) {
	mockExecutor := &commandmocks.MockExecutor{}
	mockNodeAcceleratorCommands(t, mockExecutor, "{}", nil)
	p := &GPUAcceleratorPlugin{executor: mockExecutor}

	response, err := p.DetectStaticNodeAccelerator(context.Background(), staticNodeAcceleratorTestRequest())

	require.NoError(t, err)
	require.NotNil(t, response)
	assert.False(t, response.Matched)
	assert.Nil(t, response.Accelerator)
	mockExecutor.AssertExpectations(t)
}

func TestGPUAcceleratorPluginDetectStaticNodeAcceleratorReturnsPluginError(t *testing.T) {
	mockExecutor := &commandmocks.MockExecutor{}
	mockNodeAcceleratorCommands(t, mockExecutor, "lspci failed", errors.New("lspci failed"))
	p := &GPUAcceleratorPlugin{executor: mockExecutor}

	response, err := p.DetectStaticNodeAccelerator(context.Background(), staticNodeAcceleratorTestRequest())

	require.Error(t, err)
	assert.Nil(t, response)
	mockExecutor.AssertExpectations(t)
}

func staticNodeAcceleratorTestRequest() *v1.DetectStaticNodeAcceleratorRequest {
	return &v1.DetectStaticNodeAcceleratorRequest{
		NodeIp: "127.0.0.1",
		SSHAuth: v1.Auth{
			SSHUser:       "root",
			SSHPrivateKey: "MTIzCg==",
		},
	}
}

func mockNodeAcceleratorCommands(
	t *testing.T,
	mockExecutor *commandmocks.MockExecutor,
	lspciOutput string,
	lspciErr error,
) {
	t.Helper()

	mockExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		cmds := args.Get(2).([]string)
		assert.True(t, strings.Contains(strings.Join(cmds, " "), "uptime"))
	}).Return([]byte("{}"), nil).Once()
	mockExecutor.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		cmds := args.Get(2).([]string)
		assert.True(t, strings.Contains(strings.Join(cmds, " "), "lspci -nn"))
	}).Return([]byte(lspciOutput), lspciErr).Once()
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

func TestGPUAcceleratorPlugin_GetNodeAcceleratorInfo_ConnectionFailureExposesContext(t *testing.T) {
	mockExec := &commandmocks.MockExecutor{}
	// First call = precheck (uptime). Simulate SSH connect-refused.
	mockExec.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		cmds := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmds, " "), "uptime")
	}).Return([]byte(""), errors.New("exit status 255: ssh: connect to host 10.255.1.54 port 22: Connection refused")).Once()

	p := &GPUAcceleratorPlugin{executor: mockExec}
	_, err := p.getNodeAcceleratorInfo(context.Background(), "10.255.1.54", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "MTIzCg==",
	})

	assert.Error(t, err)
	assert.True(t, errors.Is(err, command_runner.ErrConnectionFailed),
		"errors.Is must report ErrConnectionFailed; got %v", err)
	// Plugin must NOT double-wrap connection failures — the runner already
	// constructed the full message. The plugin only propagates the error.
	assert.NotContains(t, err.Error(), "get node 10.255.1.54 pci info failed",
		"connection failure must not be wrapped with the misleading pci-info prefix")
	assert.Contains(t, err.Error(), "ssh connection failed to node",
		"connection-phase identification must reach the caller")
	assert.Contains(t, err.Error(), "10.255.1.54", "target IP must be surfaced")
	assert.Contains(t, err.Error(), "Connection refused", "underlying SSH stderr must survive")
	assert.Contains(t, err.Error(), "hint:", "static-cluster hint must be present")
	mockExec.AssertExpectations(t)
}

func TestGPUAcceleratorPlugin_GetNodeAcceleratorInfo_CommandFailureKeepsPCIWording(t *testing.T) {
	mockExec := &commandmocks.MockExecutor{}
	// First call = precheck (uptime) — success.
	mockExec.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		cmds := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmds, " "), "uptime")
	}).Return([]byte("ok"), nil).Once()
	// Second call = lspci — fails with exit status (i.e., post-connection failure).
	mockExec.On("Execute", mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		cmds := args.Get(2).([]string)
		assert.Contains(t, strings.Join(cmds, " "), "lspci -nn")
	}).Return([]byte("lspci: command not found"), errors.New("exit status 127")).Once()

	p := &GPUAcceleratorPlugin{executor: mockExec}
	_, err := p.getNodeAcceleratorInfo(context.Background(), "10.0.0.5", v1.Auth{
		SSHUser:       "root",
		SSHPrivateKey: "MTIzCg==",
	})

	assert.Error(t, err)
	assert.False(t, errors.Is(err, command_runner.ErrConnectionFailed),
		"post-connection command failure must not look like a connection failure")
	assert.Contains(t, err.Error(), "get node 10.0.0.5 pci info failed",
		"command-phase failure must keep the pci-info wording")
	assert.NotContains(t, err.Error(), "hint:",
		"command-phase failure must not carry the static-cluster hint (it is connection-specific)")
	mockExec.AssertExpectations(t)
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
	require.NotNil(t, profile.EngineRuntime)
	require.NotNil(t, profile.MetricsExporter)
	assert.Equal(t, string(v1.AcceleratorTypeNVIDIAGPU), profile.AcceleratorType)
	assert.Equal(t, "nvidia", profile.ClusterRuntime.Runtime)
	assert.Equal(t, []string{"--gpus all"}, profile.ClusterRuntime.Options)
	assert.Equal(t, nvidiaGPUNodeRuntimeConfig(), *profile.ClusterRuntime)
	require.NotNil(t, profile.EngineRuntime)
	assert.Equal(t, "nvidia", profile.EngineRuntime.Runtime)
	containerRuntime, err := p.GetContainerRuntimeConfig()
	require.NoError(t, err)
	assert.Equal(t, containerRuntime, *profile.EngineRuntime)
	assert.Equal(t, "dcgm-exporter", profile.MetricsExporter.Name)
	assert.Equal(t, nvidiaDCGMExporterImage, profile.MetricsExporter.Image)
	assert.Equal(t, nvidiaDCGMExporterPort, profile.MetricsExporter.Port)
	assert.Equal(t, map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}, profile.MetricsExporter.Env)
	assert.Equal(t,
		[]string{
			"--collectors",
			nvidiaDCGMExporterCollectorsPath,
			"--address",
			":19400",
		},
		profile.MetricsExporter.Args)
	require.NotNil(t, profile.MetricsExporter.Runtime)
	assert.True(t, profile.MetricsExporter.Runtime.HostNetwork)
	require.NotNil(t, profile.MetricsExporter.Runtime.Capabilities)
	assert.Equal(t, []string{"SYS_ADMIN"}, profile.MetricsExporter.Runtime.Capabilities.Add)
	assert.Equal(t,
		map[string]string{NvidiaGPUDiscoveryLabelKey: NvidiaGPUDiscoveryLabelValue},
		profile.MetricsExporter.Runtime.NodeSelector)
	assert.Equal(t, []string{"--gpus all"}, profile.MetricsExporter.Runtime.DockerRunOptions)
	require.Len(t, profile.MetricsExporter.ConfigFiles, 1)
	assert.Equal(t, nvidiaDCGMExporterCollectorsPath, profile.MetricsExporter.ConfigFiles[0].Path)
	collectors := profile.MetricsExporter.ConfigFiles[0].Content
	for _, metric := range []string{
		"DCGM_FI_DEV_GPU_UTIL",
		"DCGM_FI_DEV_GPU_NAME",
		"DCGM_FI_DEV_NAME",
		"DCGM_FI_DEV_BRAND",
		"DCGM_FI_DEV_NVML_INDEX",
		"DCGM_FI_DEV_GPU_UUID",
		"DCGM_FI_DEV_GPU_MINOR_NUMBER",
		"DCGM_FI_DEV_FB_USED",
		"DCGM_FI_DEV_FB_TOTAL",
		"DCGM_FI_DEV_FB_USED_PERCENT",
		"DCGM_FI_DEV_PCI_BUS_ID",
		"DCGM_FI_DEV_PCI_BUSID",
		"DCGM_FI_CUDA_DRIVER_VERSION",
		"DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY",
		"DCGM_FI_DEV_PCIE_MAX_LINK_GEN",
		"DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH",
		"DCGM_FI_DEV_PCIE_LINK_GEN",
		"DCGM_FI_DEV_PCIE_LINK_WIDTH",
		"DCGM_FI_DEV_XID_ERRORS",
		"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL",
		"DCGM_FI_DEV_RETIRED_PENDING",
		"DCGM_FI_DEV_PCIE_REPLAY_COUNTER",
		"DCGM_FI_PROF_GR_ENGINE_ACTIVE",
		"DCGM_FI_PROF_SM_ACTIVE",
		"DCGM_FI_PROF_SM_OCCUPANCY",
		"DCGM_FI_PROF_PIPE_TENSOR_ACTIVE",
		"DCGM_FI_PROF_PIPE_FP64_ACTIVE",
		"DCGM_FI_PROF_PIPE_FP32_ACTIVE",
		"DCGM_FI_PROF_PIPE_FP16_ACTIVE",
		"DCGM_FI_PROF_DRAM_ACTIVE",
		"DCGM_FI_PROF_PCIE_TX_BYTES",
		"DCGM_FI_PROF_PCIE_RX_BYTES",
		"DCGM_FI_PROF_NVLINK_RX_BYTES",
		"DCGM_FI_PROF_NVLINK_TX_BYTES",
		"DCGM_FI_DEV_POWER_VIOLATION",
		"DCGM_FI_DEV_THERMAL_VIOLATION",
	} {
		assert.Contains(t, collectors, metric)
	}
	assert.NotContains(t, collectors, "DCGM_CUSTOM_")
	assert.NotContains(t, collectors, "DCGM_FI_DEV_CLOCKS_EVENT_REASONS")
	assert.NotContains(t, collectors, "DCGM_FI_DEV_P2P_NVLINK_STATUS")
}
