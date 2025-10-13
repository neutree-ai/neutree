package plugin

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandmocks "github.com/neutree-ai/neutree/pkg/command/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

var (
	testLspciOuput = `00:00.0 Host bridge [0600]: Intel Corporation 82G33/G31/P35/P31 Express DRAM Controller [8086:29c0]
			00:01.0 VGA compatible controller [0300]: Red Hat, Inc. Virtio GPU [1af4:1050] (rev 01)
			00:02.0 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.1 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.2 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.3 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.4 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.5 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.6 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:02.7 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			00:03.0 Host bridge [0600]: Red Hat, Inc. QEMU PCIe Expander bridge [1b36:000b]
			00:1f.0 ISA bridge [0601]: Intel Corporation 82801IB (ICH9) LPC Interface Controller [8086:2918] (rev 02)
			00:1f.2 SATA controller [0106]: Intel Corporation 82801IR/IO/IH (ICH9R/DO/DH) 6 port SATA Controller [AHCI mode] [8086:2922] (rev 02)
			00:1f.3 SMBus [0c05]: Intel Corporation 82801I (ICH9 Family) SMBus Controller [8086:2930] (rev 02)
			01:00.0 Ethernet controller [0200]: Red Hat, Inc. Virtio network device [1af4:1041] (rev 01)
			02:00.0 Ethernet controller [0200]: Red Hat, Inc. Virtio network device [1af4:1041] (rev 01)
			03:00.0 SCSI storage controller [0100]: Red Hat, Inc. Virtio SCSI [1af4:1048] (rev 01)
			04:00.0 USB controller [0c03]: Red Hat, Inc. QEMU XHCI Host Controller [1b36:000d] (rev 01)
			05:00.0 SCSI storage controller [0100]: Red Hat, Inc. Virtio block device [1af4:1042] (rev 01)
			06:00.0 SCSI storage controller [0100]: Red Hat, Inc. Virtio block device [1af4:1042] (rev 01)
			07:00.0 SCSI storage controller [0100]: Red Hat, Inc. Virtio block device [1af4:1042] (rev 01)
			08:00.0 Unclassified device [00ff]: Red Hat, Inc. Virtio memory balloon [1af4:1045] (rev 01)
			80:00.0 PCI bridge [0604]: Red Hat, Inc. QEMU PCIe Root port [1b36:000c]
			81:00.0 PCI bridge [0604]: Texas Instruments XIO3130 PCI Express Switch (Upstream) [104c:8232] (rev 02)
			82:00.0 PCI bridge [0604]: Texas Instruments XIO3130 PCI Express Switch (Downstream) [104c:8233] (rev 01)
			83:00.0 Processing accelerators [1200]: Advanced Micro Devices, Inc. [AMD/ATI] Device [1002:74b5]`
)

func TestAMDGPUAcceleratorPlugin_BasicMethods(t *testing.T) {
	p := &AMDGPUAcceleratorPlugin{}
	// Test basic interface methods
	assert.Equal(t, v1.AcceleratorTypeAMDGPU, p.Resource())
	assert.Equal(t, p, p.Handle())
	assert.Equal(t, InternalPluginType, p.Type())
}

func TestAMDGPUAcceleratorPlugin_GetSupportEngines(t *testing.T) {
	p := &AMDGPUAcceleratorPlugin{}
	// Test GetSupportEngines method
	response, err := p.GetSupportEngines(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, response)
	assert.Len(t, response.Engines, 2)

	var engines []string
	for _, engine := range response.Engines {
		engines = append(engines, engine.Metadata.Name)
	}
	assert.Contains(t, engines, "vllm")
	assert.Contains(t, engines, "llama-cpp")
}

func TestAMDGPUAcceleratorPlugin_GetNodeAcceleratorInfo(t *testing.T) {
	tests := []struct {
		name                    string
		mockSetup               func(*commandmocks.MockExecutor)
		expecteAcceleratorCount int
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
			expecteAcceleratorCount: 0,
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
				}).Return([]byte(testLspciOuput), nil).Once()
			},
			expecteAcceleratorCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExector := &commandmocks.MockExecutor{}
			tt.mockSetup(mockExector)
			p := &AMDGPUAcceleratorPlugin{
				executor: mockExector,
			}
			// Test GetNodeAcceleratorInfo method
			accelerators, err := p.getNodeAcceleratorInfo(context.Background(), "127.0.0.1", v1.Auth{
				SSHUser:       "root",
				SSHPrivateKey: "MTIzCg==",
			})
			assert.NoError(t, err)
			assert.Len(t, accelerators, tt.expecteAcceleratorCount)
		})
	}
}

func TestAMDGPUAcceleratorPlugin_GetNodeRuntimeConfig(t *testing.T) {
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
			expectRuntimeConfig: v1.RuntimeConfig{
				ImageSuffix: "rocm",
			},
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
				}).Return([]byte(testLspciOuput), nil).Once()
			},
			expectRuntimeConfig: v1.RuntimeConfig{
				ImageSuffix: "rocm",
				Env: map[string]string{
					"ACCELERATOR_TYPE":    "amd_gpu",
					"AMD_VISIBLE_DEVICES": "all",
				},
				Runtime: "amd",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockExector := &commandmocks.MockExecutor{}
			tt.mockSetup(mockExector)
			p := &AMDGPUAcceleratorPlugin{
				executor: mockExector,
			}
			// Test GetNodeRuntimeConfig method
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

func TestAMDGPUAcceleratorPlugin_GetKubernetesContainerAcceleratorInfo(t *testing.T) {
	tests := []struct {
		name                    string
		container               corev1.Container
		expecteAcceleratorCount int
	}{
		{
			name: "Container without GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"cpu": resource.MustParse("1"),
					},
				},
			},
			expecteAcceleratorCount: 0,
		},
		{
			name: "Container with GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"amd.com/gpu": resource.MustParse("1"),
					},
				},
			},
			expecteAcceleratorCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &AMDGPUAcceleratorPlugin{}
			// Test GetKubernetesContainerAcceleratorInfo method
			accelerators := p.getKubernetesContainerAcceleratorInfo(tt.container)
			assert.Len(t, accelerators, tt.expecteAcceleratorCount)
		})
	}
}

func TestAMDGPUAcceleratorPlugin_GetKubernetesContainerRuntimeConfig(t *testing.T) {
	tests := []struct {
		name                string
		container           corev1.Container
		expectRuntimeConfig v1.RuntimeConfig
	}{
		{
			name: "Container without GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"cpu": resource.MustParse("1"),
					},
				},
			},
			expectRuntimeConfig: v1.RuntimeConfig{
				ImageSuffix: "rocm",
			},
		},
		{
			name: "Container with GPU resources",
			container: corev1.Container{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						"amd.com/gpu": resource.MustParse("1"),
					},
				},
			},
			expectRuntimeConfig: v1.RuntimeConfig{
				ImageSuffix: "rocm",
				Env: map[string]string{
					"ACCELERATOR_TYPE": "amd_gpu",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &AMDGPUAcceleratorPlugin{}
			// Test GetKubernetesContainerRuntimeConfig method
			runtimeConfig, err := p.GetKubernetesContainerRuntimeConfig(context.Background(), &v1.GetContainerRuntimeConfigRequest{
				Container: tt.container,
			})
			assert.NoError(t, err)
			assert.NotNil(t, runtimeConfig)
			assert.Equal(t, tt.expectRuntimeConfig, runtimeConfig.RuntimeConfig)
		})
	}
}
