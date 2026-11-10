package metrics

import (
	"crypto/sha256"
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ManifestVariables are shared by Legacy metrics and accelerator Operators.
type ManifestVariables struct {
	ClusterName                      string
	Workspace                        string
	Namespace                        string
	ImagePullSecret                  string
	ClusterVersion                   string
	HashSuffix                       string
	EnableNeutreeNodeAgentMetrics    bool
	NeutreeNodeAgentMetricsName      string
	NeutreeNodeAgentMetricsImage     string
	NeutreeNodeAgentMetricsPort      int
	NeutreeNodeAgentMetricsResources map[string]string
	MetricsMode                      string
	NodeAgentClusterType             string
	NodeAgentLegacyClusterType       string
	NodeAgent                        NodeAgent
	AcceleratorExporters             []AcceleratorExporter
}

func NewManifestVariables(cluster *v1.Cluster, namespace, imagePrefix, imagePullSecret string, selection NodeAgentSelection) ManifestVariables {
	mode := v1.ClusterAcceleratorExporterModeManaged
	if cluster.Spec != nil {
		mode = cluster.Spec.Config.AcceleratorExporterMode()
	}

	return ManifestVariables{
		ClusterName:                      cluster.Metadata.Name,
		Workspace:                        cluster.Metadata.Workspace,
		Namespace:                        namespace,
		ImagePullSecret:                  imagePullSecret,
		ClusterVersion:                   cluster.GetVersion(),
		HashSuffix:                       hashString(cluster.Key()),
		NeutreeNodeAgentMetricsName:      "neutree-node-agent",
		NeutreeNodeAgentMetricsImage:     rewriteImageRef(imagePrefix, selection.Image),
		NeutreeNodeAgentMetricsPort:      19101,
		NeutreeNodeAgentMetricsResources: map[string]string{"cpu": "500m", "memory": "256Mi"},
		MetricsMode:                      string(mode),
		NodeAgentClusterType:             v1.KubernetesClusterType,
		NodeAgentLegacyClusterType:       v1.KubernetesClusterType,
		NodeAgent:                        DefaultNodeAgent(),
	}
}

func hashString(value string) string {
	hash := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", hash)[:10]
}

// ClusterHashSuffix provides a stable suffix for cluster-scoped resources.
func ClusterHashSuffix(cluster *v1.Cluster) string {
	if cluster == nil {
		return ""
	}

	return hashString(cluster.Key())
}

func rewriteImageRef(prefix, image string) string {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix == "" || image == "" || isDockerHubPrefix(prefix) {
		return image
	}

	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 && (parts[0] == "localhost" || strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		image = parts[1]
	}

	return prefix + "/" + image
}

func isDockerHubPrefix(prefix string) bool {
	host := strings.SplitN(prefix, "/", 2)[0]
	switch strings.ToLower(host) {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	default:
		return false
	}
}
