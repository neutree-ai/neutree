package endpoint

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ComputeEndpointSpecHash computes a SHA256 hash from user-controlled endpoint spec fields.
// Only fields that the user directly controls are included. The Cluster field is excluded
// because it references infrastructure that may change independently (e.g., cluster version
// upgrades). This ensures infrastructure-only changes don't trigger endpoint updates.
func ComputeEndpointSpecHash(spec *v1.EndpointSpec) string {
	if spec == nil {
		return ""
	}

	// Hash only user-controlled fields from the endpoint spec.
	// Cluster is excluded — it's an infra reference, not a user-tunable parameter.
	hashInput := struct {
		Engine            *v1.EndpointEngineSpec `json:"engine,omitempty"`
		Model             *v1.ModelSpec          `json:"model,omitempty"`
		Replicas          v1.ReplicaSpec         `json:"replicas,omitempty"`
		Resources         *v1.ResourceSpec       `json:"resources,omitempty"`
		Env               map[string]string      `json:"env,omitempty"`
		Variables         map[string]interface{} `json:"variables,omitempty"`
		DeploymentOptions map[string]interface{} `json:"deployment_options,omitempty"`
	}{
		Engine:            spec.Engine,
		Model:             spec.Model,
		Replicas:          spec.Replicas,
		Resources:         spec.Resources,
		Env:               spec.Env,
		Variables:         spec.Variables,
		DeploymentOptions: spec.DeploymentOptions,
	}

	data, err := json.Marshal(hashInput)
	if err != nil {
		klog.Warningf("ComputeEndpointSpecHash: failed to marshal spec: %v", err)
		return ""
	}

	hash := sha256.Sum256(data)

	return fmt.Sprintf("%x", hash)
}
