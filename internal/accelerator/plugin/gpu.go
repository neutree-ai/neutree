package plugin

import (
	"context"
	"encoding/base64"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
)

const (
	NvidiaGPUKubernetesResource        corev1.ResourceName = "nvidia.com/gpu"
	NvidiaGPUMemoryResource            corev1.ResourceName = "nvidia.com/gpumem"
	NvidiaGPUMemoryPercentageResource  corev1.ResourceName = "nvidia.com/gpumem-percentage"
	NvidiaGPUCoreResource              corev1.ResourceName = "nvidia.com/gpucores"
	NvidiaGPUCountResource             string              = "nvidia.com/gpu.count"
	NvidiaGPUKubernetesNodeSelectorKey string              = "nvidia.com/gpu.product"
	nvidiaDCGMExporterImage            string              = "nvcr.io/nvidia/k8s/dcgm-exporter:3.3.9-3.6.1-ubuntu22.04"
	nvidiaDCGMExporterPort             int                 = 9400
	nvidiaDCGMExporterCollectorsPath   string              = "/etc/neutree/dcgm-exporter/default-counters.csv"
	nvidiaDCGMExporterCollectors       string              = `# Format
# DCGM FIELD, Prometheus metric type, help message
DCGM_FI_DEV_SM_CLOCK, gauge, SM clock frequency (in MHz).
DCGM_FI_DEV_MEM_CLOCK, gauge, Memory clock frequency (in MHz).
DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature (in C).
DCGM_FI_DEV_POWER_USAGE, gauge, Power draw (in W).
DCGM_FI_DEV_GPU_UTIL, gauge, GPU utilization (in %).
DCGM_FI_DEV_MEM_COPY_UTIL, gauge, Memory utilization (in %).
DCGM_FI_DEV_FB_FREE, gauge, Frame buffer memory free (in MB).
DCGM_FI_DEV_FB_USED, gauge, Frame buffer memory used (in MB).
DCGM_FI_DEV_FB_TOTAL, gauge, Total frame buffer memory (in MB).
DCGM_FI_DEV_FB_RESERVED, gauge, Frame buffer memory reserved (in MB).
DCGM_FI_DEV_FB_USED_PERCENT, gauge, Frame buffer memory used ratio.
DCGM_FI_DEV_BAR1_FREE, gauge, BAR1 memory free (in MB).
DCGM_FI_DEV_BAR1_USED, gauge, BAR1 memory used (in MB).
DCGM_FI_DEV_BAR1_TOTAL, gauge, Total BAR1 memory (in MB).
DCGM_FI_DEV_MEMORY_TEMP, gauge, Memory temperature (in C).
DCGM_FI_DEV_NAME, label, Name of the GPU device.
DCGM_FI_DEV_BRAND, label, Device brand.
DCGM_FI_DEV_PCI_BUSID, label, PCI attributes for the device.
DCGM_FI_CUDA_DRIVER_VERSION, gauge, CUDA driver version.
DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY, gauge, CUDA compute capability for the device.
DCGM_FI_DEV_PCIE_LINK_GEN, gauge, PCIe current link generation.
DCGM_FI_DEV_PCIE_LINK_WIDTH, gauge, PCIe current link width.
DCGM_FI_DEV_NVLINK_BANDWIDTH_TOTAL, counter, Total NVLink bandwidth counter.
DCGM_FI_PROF_GR_ENGINE_ACTIVE, gauge, Ratio of time the graphics or compute engine is active.
DCGM_FI_PROF_SM_ACTIVE, gauge, Ratio of cycles an SM has at least one active warp.
DCGM_FI_PROF_SM_OCCUPANCY, gauge, Ratio of resident warps on an SM.
DCGM_FI_PROF_PIPE_TENSOR_ACTIVE, gauge, Ratio of cycles the tensor pipe is active.
DCGM_FI_PROF_PIPE_FP64_ACTIVE, gauge, Ratio of cycles the FP64 pipe is active.
DCGM_FI_PROF_PIPE_FP32_ACTIVE, gauge, Ratio of cycles the FP32 pipe is active.
DCGM_FI_PROF_PIPE_FP16_ACTIVE, gauge, Ratio of cycles the FP16 pipe is active.
DCGM_FI_PROF_DRAM_ACTIVE, gauge, Ratio of cycles the device memory interface is active.
DCGM_FI_PROF_PCIE_TX_BYTES, counter, Total number of bytes transmitted through PCIe TX via NVML.
DCGM_FI_PROF_PCIE_RX_BYTES, counter, Total number of bytes received through PCIe RX via NVML.
DCGM_FI_PROF_NVLINK_RX_BYTES, counter, Total number of bytes received through NVLink.
DCGM_FI_PROF_NVLINK_TX_BYTES, counter, Total number of bytes transmitted through NVLink.
DCGM_FI_DEV_XID_ERRORS, gauge, Value of the last XID error encountered.
DCGM_FI_DEV_ECC_SBE_VOL_TOTAL, counter, Volatile single-bit ECC errors.
DCGM_FI_DEV_ECC_DBE_VOL_TOTAL, counter, Volatile double-bit ECC errors.
DCGM_FI_DEV_ECC_DBE_AGG_TOTAL, counter, Aggregate double-bit ECC errors.
DCGM_FI_DEV_RETIRED_DBE, gauge, Retired pages due to double-bit ECC errors.
DCGM_FI_DEV_RETIRED_PENDING, gauge, Pending retired pages.
DCGM_FI_DEV_PCIE_REPLAY_COUNTER, counter, PCIe replay counter.
DCGM_FI_DEV_NVLINK_CRC_FLIT_ERROR_COUNT_TOTAL, counter, Total NVLink CRC flit errors.
DCGM_FI_DEV_NVLINK_REPLAY_ERROR_COUNT_TOTAL, counter, Total NVLink replay errors.
DCGM_FI_DEV_POWER_VIOLATION, counter, Power violation counter.
DCGM_FI_DEV_THERMAL_VIOLATION, counter, Thermal violation counter.
DCGM_FI_DEV_PSTATE, gauge, GPU performance state.
DCGM_FI_DRIVER_VERSION, label, Driver Version.
`
	NvidiaGPUMemoryNodeLabelKey      string = "nvidia.com/gpu.memory"
	NvidiaGPUVirtualizationLabelKey  string = "neutree.ai/nvidia-vgpu-enabled"
	NvidiaGPUDiscoveryLabelKey       string = "nvidia.com/gpu.present"
	NvidiaGPUDiscoveryLabelValue     string = "true"
	NvidiaGPUTopologyAwarePolicy     string = "topology-aware"
	NvidiaGPUDefaultDeviceSplitCount int    = 100
	NvidiaGPUOperatorDriverRoot      string = "/run/nvidia/driver"
)

func init() { //nolint:gochecknoinits
	registerAcceleratorPlugin(&GPUAcceleratorPlugin{
		executor: &command.OSExecutor{},
	})
}

type GPUAcceleratorPlugin struct {
	executor command.Executor
}

func (p *GPUAcceleratorPlugin) Resource() string {
	return string(v1.AcceleratorTypeNVIDIAGPU)
}

func (p *GPUAcceleratorPlugin) Handle() AcceleratorPluginHandle {
	return p
}

func (p *GPUAcceleratorPlugin) GetNodeAccelerator(ctx context.Context,
	request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	resp := &v1.GetNodeAcceleratorResponse{}

	accelerators, err := p.getNodeAcceleratorInfo(ctx, request.NodeIp, request.SSHAuth)
	if err != nil {
		return nil, err
	}

	resp.Accelerators = accelerators

	return resp, nil
}

func (p *GPUAcceleratorPlugin) GetNodeRuntimeConfig(ctx context.Context,
	request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	accelerators, err := p.getNodeAcceleratorInfo(ctx, request.NodeIp, request.SSHAuth)
	if err != nil {
		return nil, err
	}

	if len(accelerators) == 0 {
		return &v1.GetNodeRuntimeConfigResponse{}, nil
	}

	return &v1.GetNodeRuntimeConfigResponse{
		RuntimeConfig: v1.RuntimeConfig{
			Runtime: "nvidia",
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
			Options: []string{"--gpus all"},
		},
	}, nil
}

func (p *GPUAcceleratorPlugin) DetectStaticNodeAccelerator(
	ctx context.Context,
	runner NodeCommandRunner,
) (*v1.StaticNodeAcceleratorStatus, bool, error) {
	return detectPCIStaticNodeAccelerator(ctx, runner, pciStaticNodeAcceleratorDetector{
		acceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		vendor:          "nvidia",
		productName:     "NVIDIA GPU",
		productModel:    "nvidia_gpu",
		match: func(line string) bool {
			return strings.Contains(line, "10de:") &&
				(strings.Contains(line, "3d controller") || strings.Contains(line, "vga compatible controller"))
		},
	})
}

func (p *GPUAcceleratorPlugin) RuntimeProfile(
	ctx context.Context,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, bool, error) {
	if accelerator.Type != p.Resource() {
		return nil, false, nil
	}

	profile, err := p.GetAcceleratorProfile(ctx)
	if err != nil {
		return nil, false, err
	}

	return profile, profile != nil, nil
}

func (p *GPUAcceleratorPlugin) getNodeAcceleratorInfo(ctx context.Context, nodeIP string, auth v1.Auth) ([]v1.Accelerator, error) {
	decodedKey, err := base64.StdEncoding.DecodeString(auth.SSHPrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "decode ssh key failed")
	}

	tmpDir, err := os.MkdirTemp("", nodeIP+"-ssh-key-")
	if err != nil {
		return nil, errors.Wrap(err, "create tmp dir failed")
	}
	defer os.RemoveAll(tmpDir)

	sshKeyPath := path.Join(tmpDir, "ssh_key")
	if err = os.WriteFile(sshKeyPath, decodedKey, 0600); err != nil {
		return nil, errors.Wrap(err, "write ssh key failed")
	}

	sshRunner := command_runner.NewSSHCommandRunner(nodeIP, nodeIP, v1.Auth{
		SSHUser:       auth.SSHUser,
		SSHPrivateKey: sshKeyPath,
	}, "", p.executor.Execute)

	// Use lspci instead of nvidia-smi to detect GPU hardware.
	// lspci reads PCI bus info directly, independent of driver status,
	// avoiding race conditions during boot when nvidia driver is still loading.
	output, err := sshRunner.Run(ctx, "lspci -nn", true, nil, true, nil, "", false)
	if err != nil {
		if errors.Is(err, command_runner.ErrConnectionFailed) {
			// The runner already produced an actionable message including the
			// target IP, underlying SSH stderr, and static-cluster hint.
			return nil, err
		}

		return nil, errors.Wrapf(err, "get node %s pci info failed", nodeIP)
	}

	var accelerators []v1.Accelerator

	lines := strings.Split(output, "\n")
	count := 0

	for _, line := range lines {
		lineLower := strings.ToLower(line)
		if !strings.Contains(lineLower, "10de:") {
			continue
		}

		if strings.Contains(lineLower, "3d controller") || strings.Contains(lineLower, "vga compatible controller") {
			accelerators = append(accelerators, v1.Accelerator{
				Type: "",
				ID:   strconv.Itoa(count),
			})
			count++
		}
	}

	return accelerators, nil
}

func (p *GPUAcceleratorPlugin) GetContainerRuntimeConfig() (v1.RuntimeConfig, error) {
	return v1.RuntimeConfig{
		Runtime: "nvidia",
		Options: []string{"--gpus all"},
	}, nil
}

func (p *GPUAcceleratorPlugin) GetAcceleratorProfile(ctx context.Context) (*v1.AcceleratorProfile, error) {
	return &v1.AcceleratorProfile{
		AcceleratorType: string(v1.AcceleratorTypeNVIDIAGPU),
		ClusterRuntime: &v1.RuntimeConfig{
			Runtime: "nvidia",
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
			Options: []string{"--gpus all"},
		},
		EndpointRuntime: &v1.RuntimeConfig{
			Runtime: "nvidia",
			Options: []string{"--gpus all"},
		},
		Metrics: &v1.AcceleratorMetricsProfile{
			Exporter: &v1.AcceleratorExporterProfile{
				Kind:          "dcgm-exporter",
				ComponentType: v1.NodeComponentTypeAcceleratorExporter,
				Image:         nvidiaDCGMExporterImage,
				Args:          []string{"--collectors", nvidiaDCGMExporterCollectorsPath},
				Port:          nvidiaDCGMExporterPort,
				ConfigFiles: []v1.NodeComponentConfigFile{
					{
						Path:         nvidiaDCGMExporterCollectorsPath,
						Content:      nvidiaDCGMExporterCollectors,
						Mode:         "0644",
						Owner:        "root",
						Group:        "root",
						Sudo:         true,
						Atomic:       true,
						CreateParent: true,
					},
				},
				Runtime: &v1.AcceleratorExporterRuntimeProfile{
					HostNetwork: true,
					Capabilities: &v1.AcceleratorExporterCapabilities{
						Add: []string{"SYS_ADMIN"},
					},
					NodeSelector: map[string]string{
						NvidiaGPUDiscoveryLabelKey: NvidiaGPUDiscoveryLabelValue,
					},
					DockerRunOptions: []string{"--gpus all"},
				},
			},
		},
		ResourceDefaults: &v1.AcceleratorResourceDefaults{
			RayResourceName:        "GPU",
			KubernetesResourceName: string(NvidiaGPUKubernetesResource),
		},
	}, nil
}

func (p *GPUAcceleratorPlugin) Ping(ctx context.Context) error {
	return nil
}

func (p *GPUAcceleratorPlugin) Type() string {
	return InternalPluginType
}

func (p *GPUAcceleratorPlugin) GetResourceConverter() ResourceConverter {
	return NewGPUConverter()
}

func (p *GPUAcceleratorPlugin) GetResourceParser() resourceparser.ResourceParser {
	return &GPUResourceParser{}
}
