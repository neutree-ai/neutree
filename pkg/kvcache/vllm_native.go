package kvcache

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type VLLMNative struct{}

type VLLMNativeOptions struct {
	CPUMemoryGiB float64 `json:"cpu_memory_gib"`
}

func (VLLMNative) Validate(endpoint *v1.Endpoint, cluster *v1.Cluster) error {
	if cluster.Spec.Type != v1.KubernetesClusterType || endpoint.Spec.Engine == nil || endpoint.Spec.Engine.Engine != "vllm" {
		return fmt.Errorf("native KV cache requires vLLM on Kubernetes")
	}

	var options VLLMNativeOptions

	if err := DecodeOptions(endpoint.Spec.KVCache.Options, &options); err != nil {
		return fmt.Errorf("native KV cache options: %w", err)
	}

	if math.IsNaN(options.CPUMemoryGiB) || math.IsInf(options.CPUMemoryGiB, 0) || options.CPUMemoryGiB <= 0 {
		return fmt.Errorf("cpu_memory_gib must be positive")
	}

	if endpoint.Spec.Resources == nil || endpoint.Spec.Resources.Memory == nil {
		return fmt.Errorf("set inference memory before enabling CPU KV cache")
	}

	if options.CPUMemoryGiB >= endpoint.Spec.Resources.GetMemoryInGB() {
		return fmt.Errorf("CPU KV cache must leave room within inference memory for the engine")
	}

	if args, ok := endpoint.Spec.Variables["engine_args"].(map[string]interface{}); ok {
		for name := range args {
			if vllmNativeManagedArg(name) {
				return fmt.Errorf("remove custom %s before enabling managed KV cache", name)
			}
		}
	}

	return nil
}

func vllmNativeManagedArg(arg string) bool {
	name := strings.SplitN(strings.ReplaceAll(strings.TrimLeft(arg, "-"), "_", "-"), "=", 2)[0]

	return name == "kv-transfer-config" || name == "kv-offloading-size" || name == "kv-offloading-backend"
}

func (n VLLMNative) Configure(endpoint *v1.Endpoint, cluster *v1.Cluster, dep *appsv1.Deployment) error {
	if err := n.Validate(endpoint, cluster); err != nil {
		return err
	}

	var options VLLMNativeOptions

	if err := DecodeOptions(endpoint.Spec.KVCache.Options, &options); err != nil {
		return err
	}

	for i := range dep.Spec.Template.Spec.Containers {
		container := &dep.Spec.Template.Spec.Containers[i]
		if container.Name != "vllm" {
			continue
		}

		for _, arg := range append(append([]string{}, container.Command...), container.Args...) {
			if vllmNativeManagedArg(arg) {
				return fmt.Errorf("engine template already supplies KV cache configuration")
			}
		}

		container.Args = append(container.Args, "--kv-offloading-backend", "native", "--kv-offloading-size", strconv.FormatFloat(options.CPUMemoryGiB, 'f', -1, 64))

		return nil
	}

	return fmt.Errorf("vLLM container not found in engine deployment")
}
