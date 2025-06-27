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
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
)

const (
	nvidiaGPUKubernetesResourceName = "nvidia.com/gpu"
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
	return "gpu"
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

	output, err := sshRunner.Run(ctx, "nvidia-smi --query-gpu=name,uuid --format=csv,noheader", true, nil, true, nil, "", false)
	// if the node is not a GPU node, 'nvidia-smi' command will return an error, so ignore the command exec error.
	// but also we need check the connect failed error.
	if err != nil {
		if err == command_runner.ErrConnectionFailed {
			return nil, errors.Wrapf(err, "connect to node %s failed", nodeIP)
		}

		klog.V(4).ErrorS(err, "run command failed", "output", output)

		return nil, nil
	}

	var accelerators []v1.Accelerator

	gpuInfoList := strings.Split(output, "\n")
	for i := 0; i < len(gpuInfoList); i++ {
		tmp := strings.Split(strings.ReplaceAll(gpuInfoList[i], " ", ""), ",")
		if len(tmp) == 2 {
			accelerators = append(accelerators, v1.Accelerator{
				Type: tmp[0],
				ID:   tmp[1],
			})
		}
	}

	return accelerators, nil
}

func (p *GPUAcceleratorPlugin) GetKubernetesContainerAccelerator(ctx context.Context,
	request *v1.GetContainerAcceleratorRequest) (*v1.GetContainerAcceleratorResponse, error) {
	resp := &v1.GetContainerAcceleratorResponse{}

	resp.Accelerators = p.getKubernetesContainerAcceleratorInfo(request.Container)

	return resp, nil
}

func (p *GPUAcceleratorPlugin) GetKubernetesContainerRuntimeConfig(ctx context.Context,
	request *v1.GetContainerRuntimeConfigRequest) (*v1.GetContainerRuntimeConfigResponse, error) {
	acclerators := p.getKubernetesContainerAcceleratorInfo(request.Container)

	if len(acclerators) == 0 {
		return &v1.GetContainerRuntimeConfigResponse{}, nil
	}

	return &v1.GetContainerRuntimeConfigResponse{
		RuntimeConfig: v1.RuntimeConfig{
			Env: map[string]string{
				"ACCELERATOR_TYPE": "gpu",
			},
		},
	}, nil
}

func (p *GPUAcceleratorPlugin) getKubernetesContainerAcceleratorInfo(container corev1.Container) []v1.Accelerator {
	var accelerators []v1.Accelerator

	for k, v := range container.Resources.Requests {
		if k == nvidiaGPUKubernetesResourceName {
			for i := 0; i < int(v.Value()); i++ {
				accelerators = append(accelerators, v1.Accelerator{
					Type: k.String(),
					ID:   strconv.Itoa(i + 1),
				})
			}
		}
	}

	return accelerators
}

func (p *GPUAcceleratorPlugin) GetSupportEngines(ctx context.Context) (*v1.GetSupportEnginesResponse, error) {
	llamaCppV1Engine := &v1.Engine{
		APIVersion: "v1",
		Kind:       "Engine",
		Metadata: &v1.Metadata{
			Name: "llama-cpp",
		},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v1",
					ValuesSchema: map[string]interface{}{
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type":    "object",
						"properties": map[string]interface{}{
							"n_threads": map[string]interface{}{
								"type": "number",
							},
						},
					},
				},
			},
			SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask},
		},
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
					Version: "v1",
					ValuesSchema: map[string]interface{}{
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type":    "object",
						"properties": map[string]interface{}{
							"dtype": map[string]interface{}{
								"type": "string",
							},
							"gpu_memory_utilization": map[string]interface{}{
								"type": "number",
							},
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

func (p *GPUAcceleratorPlugin) Ping(ctx context.Context) error {
	return nil
}

func (p *GPUAcceleratorPlugin) Type() string {
	return InternalPluginType
}
