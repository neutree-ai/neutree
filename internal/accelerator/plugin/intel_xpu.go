package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
)

const (
	// TODO: Intel XPU Kubernetes support is not implemented yet
	IntelXPUKubernetesResource = "intel.com/xpu"
)

func init() { //nolint: gochecknoinits
	registerAcceleratorPlugin(&IntelXPUAcceleratorPlugin{
		executor: &command.OSExecutor{},
	})
}

type IntelXPUAcceleratorPlugin struct {
	executor command.Executor
}

func (p *IntelXPUAcceleratorPlugin) Handle() AcceleratorPluginHandle {
	return p
}

func (p *IntelXPUAcceleratorPlugin) Resource() string {
	return "intel-xpu"
}

func (p *IntelXPUAcceleratorPlugin) Type() string {
	return InternalPluginType
}

func (p *IntelXPUAcceleratorPlugin) GetNodeAccelerator(ctx context.Context,
	request *v1.GetNodeAcceleratorRequest) (*v1.GetNodeAcceleratorResponse, error) {
	resp := &v1.GetNodeAcceleratorResponse{}

	accelerators, err := p.getNodeAcceleratorInfo(ctx, request.NodeIp, request.SSHAuth)
	if err != nil {
		return nil, err
	}

	resp.Accelerators = accelerators

	return resp, nil
}

func (p *IntelXPUAcceleratorPlugin) GetNodeRuntimeConfig(ctx context.Context,
	request *v1.GetNodeRuntimeConfigRequest) (*v1.GetNodeRuntimeConfigResponse, error) {
	accelerators, err := p.getNodeAcceleratorInfo(ctx, request.NodeIp, request.SSHAuth)
	if err != nil {
		return nil, err
	}

	if len(accelerators) == 0 {
		return &v1.GetNodeRuntimeConfigResponse{
			RuntimeConfig: v1.RuntimeConfig{
				ImageSuffix: "xpu",
			},
		}, nil
	}

	return &v1.GetNodeRuntimeConfigResponse{
		RuntimeConfig: v1.RuntimeConfig{
			ImageSuffix: "xpu",
			Env: map[string]string{
				"ACCELERATOR_TYPE":   "intel_xpu",
				"SYCL_DEVICE_FILTER": "level_zero:gpu",
			},
			Options: []string{"--device /dev/dri", "-v /dev/dri/by-path:/dev/dri/by-path"},
		},
	}, nil
}

func (p *IntelXPUAcceleratorPlugin) getNodeAcceleratorInfo(ctx context.Context, nodeIP string, auth v1.Auth) ([]v1.Accelerator, error) {
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

	// Use xpu-smi to detect Intel XPU devices
	output, err := sshRunner.Run(ctx, "xpu-smi discovery -j", true, nil, true, nil, "", false)
	if err != nil {
		return nil, errors.Wrapf(err, "get node %s Intel XPU info failed", nodeIP)
	}

	// Remove any non-JSON prefix from output
	// Only keep from the first '{' character
	importStrings := false
	// workaround for go import
	_ = importStrings

	idx := strings.Index(output, "{")
	if idx >= 0 {
		output = output[idx:]
	}

	var xpuDiscovery struct {
		DeviceList []struct {
			DeviceID           int    `json:"device_id"`
			DeviceName         string `json:"device_name"`
			DeviceType         string `json:"device_type"`
			DeviceFunctionType string `json:"device_function_type"`
			UUID               string `json:"uuid"`
			VendorName         string `json:"vendor_name"`
		} `json:"device_list"`
	}

	if err := json.Unmarshal([]byte(output), &xpuDiscovery); err != nil {
		return nil, errors.Wrap(err, "failed to parse xpu-smi output")
	}

	var accelerators []v1.Accelerator

	for _, device := range xpuDiscovery.DeviceList {
		// Only count GPU devices with physical function type
		if device.DeviceType == "GPU" && device.DeviceFunctionType == "physical" {
			accelerators = append(accelerators, v1.Accelerator{
				Type: device.DeviceName,
				ID:   strconv.Itoa(device.DeviceID),
			})
		}
	}

	return accelerators, nil
}

func (p *IntelXPUAcceleratorPlugin) GetKubernetesContainerAccelerator(ctx context.Context,
	request *v1.GetContainerAcceleratorRequest) (*v1.GetContainerAcceleratorResponse, error) {
	// TODO: Intel XPU Kubernetes support is not implemented yet
	return nil, errors.New("Intel XPU Kubernetes support is not implemented yet")
}

func (p *IntelXPUAcceleratorPlugin) GetKubernetesContainerRuntimeConfig(ctx context.Context,
	request *v1.GetContainerRuntimeConfigRequest) (*v1.GetContainerRuntimeConfigResponse, error) {
	// TODO: Intel XPU Kubernetes support is not implemented yet
	return nil, errors.New("Intel XPU Kubernetes support is not implemented yet")
}

func (p *IntelXPUAcceleratorPlugin) GetSupportEngines(ctx context.Context) (*v1.GetSupportEnginesResponse, error) {
	vllmV1EngineSchema, err := GetVLLMV1EngineSchema()
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
					Version:      "v1",
					ValuesSchema: vllmV1EngineSchema,
				},
			},
			SupportedTasks: []string{v1.TextGenerationModelTask, v1.TextEmbeddingModelTask, v1.TextRerankModelTask},
		},
	}

	return &v1.GetSupportEnginesResponse{
		Engines: []*v1.Engine{
			vllmV1Engine,
		},
	}, nil
}

func (p *IntelXPUAcceleratorPlugin) Ping(ctx context.Context) error {
	return nil
}
