package plugin

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
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

func TestGPUAcceleratorPlugin_GetSupportEngines(t *testing.T) {
	plugin := &GPUAcceleratorPlugin{}

	response, err := plugin.GetSupportEngines(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Len(t, response.Engines, 3)

	// Check that we have expected engines
	engineNames := make(map[string]*v1.Engine)
	for _, engine := range response.Engines {
		engineNames[engine.Metadata.Name] = engine
	}

	// Verify vLLM engine
	vllmEngine, exists := engineNames["vllm"]
	assert.True(t, exists)
	assert.NotNil(t, vllmEngine.Spec.Versions[0].ValuesSchema)
	assert.Contains(t, vllmEngine.Spec.SupportedTasks, v1.TextGenerationModelTask)

	// Verify Llama.cpp engine
	llamaCppEngine, exists := engineNames["llama-cpp"]
	assert.True(t, exists)
	assert.NotNil(t, llamaCppEngine.Spec.Versions[0].ValuesSchema)
	assert.Contains(t, llamaCppEngine.Spec.SupportedTasks, v1.TextGenerationModelTask)

	// Verify SGLang engine
	sglangEngine, exists := engineNames["sglang"]
	assert.True(t, exists)
	assert.NotNil(t, sglangEngine.Spec.Versions[0].ValuesSchema)
	assert.Contains(t, sglangEngine.Spec.SupportedTasks, v1.TextGenerationModelTask)
	assert.Contains(t, sglangEngine.Spec.SupportedTasks, v1.TextEmbeddingModelTask)
}

func TestGPUAcceleratorPlugin_GetNodeAcceleratorInfo(t *testing.T) {
	tests := []struct {
		name                    string
		mockSetup               func(*commandmocks.MockExecutor)
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
