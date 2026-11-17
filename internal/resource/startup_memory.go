package resource

import (
	"regexp"
	"strconv"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	startupLogLimitBytes = int64(2 * 1024 * 1024)
	startupLogTimeout    = 3 * time.Second
)

var (
	vllmModelLoadingRegex     = regexp.MustCompile(`Model loading took ([0-9]+(?:\.[0-9]+)?)\s*(?:GiB|GB)`)
	vllmAvailableKVCacheRegex = regexp.MustCompile(`Available KV cache memory:\s*([0-9]+(?:\.[0-9]+)?)\s*(?:GiB|GB)`)
)

func isVLLMEndpoint(endpoint *v1.Endpoint) bool {
	return endpoint != nil &&
		endpoint.Spec != nil &&
		endpoint.Spec.Engine != nil &&
		endpoint.Spec.Engine.Engine == v1.EngineNameVLLM
}

func hasVLLMBlocksOverride(endpoint *v1.Endpoint) bool {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Variables == nil {
		return false
	}

	engineArgs, ok := endpoint.Spec.Variables["engine_args"].(map[string]interface{})
	if !ok {
		return false
	}

	_, overrideUnderscore := engineArgs["num_gpu_blocks_override"]
	_, overrideDash := engineArgs["num-gpu-blocks-override"]

	return overrideUnderscore || overrideDash
}

func replicaAllocationKey(replicaID, instanceID string) string {
	if replicaID != "" {
		return replicaID
	}

	return instanceID
}

func previousReplicasByKey(endpoint *v1.Endpoint) map[string]v1.ReplicaDeviceAllocation {
	prevByKey := make(map[string]v1.ReplicaDeviceAllocation)

	if endpoint == nil || endpoint.Status == nil || endpoint.Status.Resources == nil {
		return prevByKey
	}

	for _, prev := range endpoint.Status.Resources.Replicas {
		key := replicaAllocationKey(prev.ReplicaID, prev.InstanceID)
		if key != "" {
			prevByKey[key] = prev
		}
	}

	return prevByKey
}

func applyCachedStartupMemory(
	instance *EndpointInstanceResource,
	prev v1.ReplicaDeviceAllocation,
	blocksOverride bool,
) {
	if instance == nil {
		return
	}

	instance.ContainerID = prev.ContainerID
	instance.ModelWeightsMiB = prev.ModelWeightsMiB
	instance.KVCacheCapacityMiB = prev.KVCacheCapacityMiB

	if blocksOverride {
		instance.KVCacheCapacityMiB = nil
	}
}

func parseVLLMStartupMemoryMiB(logs string) (*float64, *float64) {
	return parseStartupMemoryMiB(vllmModelLoadingRegex, logs, false),
		parseStartupMemoryMiB(vllmAvailableKVCacheRegex, logs, true)
}

// Startup logs provide replica-level representative values: maximum model
// weights and minimum available KV memory among the records read.
func parseStartupMemoryMiB(pattern *regexp.Regexp, logs string, takeMin bool) *float64 {
	var value float64

	for _, match := range pattern.FindAllStringSubmatch(logs, -1) {
		gib, err := strconv.ParseFloat(match[1], 64)
		if err != nil || gib <= 0 {
			continue
		}

		if value == 0 || (takeMin && gib < value) || (!takeMin && gib > value) {
			value = gib
		}
	}

	if value == 0 {
		return nil
	}

	mib := roundFloat64ToTwoDecimals(value * 1024)

	return &mib
}
