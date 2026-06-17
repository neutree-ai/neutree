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
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
)

const (
	NvidiaGPUKubernetesResource        corev1.ResourceName = "nvidia.com/gpu"
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
DCGM_FI_DEV_XID_ERRORS, gauge, Value of the last XID error encountered.
DCGM_FI_DRIVER_VERSION, label, Driver Version.
`
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
		if err == command_runner.ErrConnectionFailed {
			return nil, errors.Wrapf(err, "connect to node %s failed", nodeIP)
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
				Volumes: []v1.NodeComponentVolume{
					{
						Name:      "dcgm-counters",
						HostPath:  nvidiaDCGMExporterCollectorsPath,
						MountPath: nvidiaDCGMExporterCollectorsPath,
						ReadOnly:  true,
					},
				},
				DockerRunOptions: []string{
					"--net=host",
					"--gpus all",
					"--cap-add=SYS_ADMIN",
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

func (p *GPUAcceleratorPlugin) GetResourceParser() ResourceParser {
	return &GPUResourceParser{}
}
