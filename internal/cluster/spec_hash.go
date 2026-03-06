package cluster

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ComputeClusterSpecHash computes a SHA256 hash of the ClusterSpec, excluding connection
// credentials (Kubeconfig, SSHPrivateKey) so that credential rotation does not trigger Updating.
func ComputeClusterSpecHash(spec *v1.ClusterSpec) string {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return ""
	}

	specCopy := &v1.ClusterSpec{}

	if err = json.Unmarshal(specJSON, specCopy); err != nil {
		return ""
	}

	// Exclude connection credentials - rotation should not trigger Updating
	if specCopy.Config != nil && specCopy.Config.KubernetesConfig != nil {
		specCopy.Config.KubernetesConfig.Kubeconfig = ""
	}

	if specCopy.Config != nil && specCopy.Config.SSHConfig != nil {
		specCopy.Config.SSHConfig.Auth.SSHPrivateKey = ""
	}

	cleanJSON, err := json.Marshal(specCopy)
	if err != nil {
		return ""
	}

	hash := sha256.Sum256(cleanJSON)

	return fmt.Sprintf("%x", hash)
}
