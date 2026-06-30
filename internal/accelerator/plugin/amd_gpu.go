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
	AMDGPUKubernetesResource        corev1.ResourceName = "amd.com/gpu"
	AMDGPUKubernetesNodeSelectorKey string              = "amd.com/gpu.product-name"
)

func init() { //nolint: gochecknoinits
	registerAcceleratorPlugin(&AMDGPUAcceleratorPlugin{
		executor: &command.OSExecutor{},
	})
}

type AMDGPUAcceleratorPlugin struct {
	executor command.Executor
}

func (p *AMDGPUAcceleratorPlugin) Handle() AcceleratorPluginHandle {
	return p
}

func (p *AMDGPUAcceleratorPlugin) Resource() string {
	return string(v1.AcceleratorTypeAMDGPU)
}

func (p *AMDGPUAcceleratorPlugin) Type() string {
	return InternalPluginType
}

func (p *AMDGPUAcceleratorPlugin) GetNodeAccelerator(ctx context.Context,
	request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	resp := &v1.GetNodeAcceleratorResponse{}

	accelerators, err := p.getNodeAcceleratorInfo(ctx, request.NodeIp, request.SSHAuth)
	if err != nil {
		return nil, err
	}

	resp.Accelerators = accelerators

	return resp, nil
}

func (p *AMDGPUAcceleratorPlugin) GetNodeRuntimeConfig(ctx context.Context,
	request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	accelerators, err := p.getNodeAcceleratorInfo(ctx, request.NodeIp, request.SSHAuth)
	if err != nil {
		return nil, err
	}

	if len(accelerators) == 0 {
		return &v1.GetNodeRuntimeConfigResponse{
			RuntimeConfig: v1.RuntimeConfig{
				ImageSuffix: "rocm",
			},
		}, nil
	}

	return &v1.GetNodeRuntimeConfigResponse{
		RuntimeConfig: v1.RuntimeConfig{
			ImageSuffix: "rocm",
			Runtime:     "amd",
			Env: map[string]string{
				"ACCELERATOR_TYPE":    "amd_gpu",
				"AMD_VISIBLE_DEVICES": "all",
			},
		},
	}, nil
}

func (p *AMDGPUAcceleratorPlugin) DetectStaticNodeAccelerator(
	ctx context.Context,
	runner NodeCommandRunner,
) (*v1.StaticNodeAcceleratorStatus, bool, error) {
	return detectPCIStaticNodeAccelerator(ctx, runner, pciStaticNodeAcceleratorDetector{
		acceleratorType: v1.AcceleratorTypeAMDGPU.String(),
		vendor:          "amd",
		productName:     "AMD GPU",
		productModel:    "amd_gpu",
		match: func(line string) bool {
			return (strings.Contains(line, "1002:") || strings.Contains(line, "advanced micro devices")) &&
				(strings.Contains(line, "processing accelerators") || strings.Contains(line, "vga compatible controller"))
		},
	})
}

func (p *AMDGPUAcceleratorPlugin) RuntimeProfile(
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

func (p *AMDGPUAcceleratorPlugin) getNodeAcceleratorInfo(ctx context.Context, nodeIP string, auth v1.Auth) ([]v1.Accelerator, error) {
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

	// amdgpu-dkms never install amd-smi or rocm-smi, so we can only use lspci to get gpu number.
	// todo: more analysis for lspci output
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
		if !strings.Contains(line, "Advanced Micro Devices") {
			continue
		}

		if strings.Contains(line, "Processing accelerators") {
			accelerators = append(accelerators, v1.Accelerator{
				Type: "",
				ID:   strconv.Itoa(count),
			})
			count++

			continue
		}

		if strings.Contains(line, "VGA compatible controller") {
			accelerators = append(accelerators, v1.Accelerator{
				Type: "",
				ID:   strconv.Itoa(count),
			})
			count++

			continue
		}
	}

	return accelerators, nil
}

func (p *AMDGPUAcceleratorPlugin) GetContainerRuntimeConfig() (v1.RuntimeConfig, error) {
	return v1.RuntimeConfig{
		Runtime: "amd",
		Env: map[string]string{
			"AMD_VISIBLE_DEVICES": "all",
		},
	}, nil
}

func (p *AMDGPUAcceleratorPlugin) GetAcceleratorProfile(ctx context.Context) (*v1.AcceleratorProfile, error) {
	return &v1.AcceleratorProfile{
		AcceleratorType: string(v1.AcceleratorTypeAMDGPU),
		ClusterRuntime: &v1.RuntimeConfig{
			ImageSuffix: "rocm",
			Runtime:     "amd",
			Env: map[string]string{
				"ACCELERATOR_TYPE":    "amd_gpu",
				"AMD_VISIBLE_DEVICES": "all",
			},
		},
	}, nil
}

func (p *AMDGPUAcceleratorPlugin) Ping(ctx context.Context) error {
	return nil
}

func (p *AMDGPUAcceleratorPlugin) GetResourceConverter() ResourceConverter {
	return NewAMDGPUConverter()
}

func (p *AMDGPUAcceleratorPlugin) GetResourceParser() resourceparser.ResourceParser {
	return &AMDGPUResourceParser{}
}

func (p *AMDGPUAcceleratorPlugin) ResolveClusterVirtualizationConfig(
	context.Context,
	*v1.Cluster,
) (*VirtualizationConfig, error) {
	return NewUnsupportedVirtualizationConfig(string(v1.AcceleratorTypeAMDGPU)), nil
}
