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

func (p *AMDGPUAcceleratorPlugin) GetSupportEngines(ctx context.Context) (*v1.GetSupportEnginesResponse, error) {
	llamaCppDefaultEngineSchema, err := GetLlamaCppDefaultEngineSchema()
	if err != nil {
		return nil, errors.Wrap(err, "failed to load Llama.cpp default engine schema")
	}

	llamaCppV1Engine := &v1.Engine{
		APIVersion: "v1",
		Kind:       "Engine",
		Metadata: &v1.Metadata{
			Name: "llama-cpp",
		},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version:      "v0.3.7",
					ValuesSchema: llamaCppDefaultEngineSchema,
					Images: map[string]*v1.EngineImage{
						"cpu": {
							ImageName: "neutree/llama-cpp-python", // no official llama-cpp-python image, so use neutree image
							Tag:       "v0.3.7",
						},
					},
					DeployTemplate: map[string]map[string]string{
						"kubernetes": {
							"default": GetLlamaCppDefaultDeployTemplate(),
						},
					},
				},
			},
			SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
		},
	}

	vllmDefaultEngineSchema, err := GetVLLMDefaultEngineSchema()
	if err != nil {
		return nil, errors.Wrap(err, "failed to load vLLM V1 engine schema")
	}

	vllmV1Engine := &v1.Engine{
		APIVersion: "v1",
		Kind:       "Engine",
		Metadata: &v1.Metadata{
			Name: "vllm",
		},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version:      "v0.8.5",
					ValuesSchema: vllmDefaultEngineSchema,
					Images: map[string]*v1.EngineImage{
						"amd_gpu": {
							ImageName: "rocm/vllm", // use official vllm image with amd gpu support
							Tag:       "rocm6.3.1_vllm_0.8.5_20250521",
						},
					},
					DeployTemplate: map[string]map[string]string{
						"kubernetes": {
							"default": GetVLLMDefaultDeployTemplate(),
						},
					},
				},
			},
			SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask, v1.TextRerankModelTask},
		},
	}

	return &v1.GetSupportEnginesResponse{
		Engines: []*v1.Engine{
			llamaCppV1Engine,
			vllmV1Engine,
		},
	}, nil
}

func (p *AMDGPUAcceleratorPlugin) Ping(ctx context.Context) error {
	return nil
}

func (p *AMDGPUAcceleratorPlugin) GetResourceConverter() ResourceConverter {
	return NewAMDGPUConverter()
}

func (p *AMDGPUAcceleratorPlugin) GetResourceParser() ResourceParser {
	return &AMDGPUResourceParser{}
}
