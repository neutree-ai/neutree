package metrics

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	"github.com/neutree-ai/neutree/internal/component"
	"github.com/neutree-ai/neutree/internal/metricsconfig"
	"github.com/neutree-ai/neutree/internal/util"
)

// MetricsManifestVariables holds the variables for rendering metrics manifests
type MetricsManifestVariables struct {
	ClusterName                      string
	Workspace                        string
	Namespace                        string
	ImagePullSecret                  string
	Version                          string
	VMAgentImage                     string
	NodeExporterName                 string
	NodeExporterImage                string
	NodeExporterPort                 int
	NeutreeNodeAgentMetricsName      string
	NeutreeNodeAgentMetricsImage     string
	NeutreeNodeAgentMetricsPort      int
	MetricsMode                      string
	NodeAgentClusterType             string
	NodeAgentLegacyClusterType       string
	NodeAgent                        metricsNodeAgent
	KubeStateMetricsImage            string
	ClusterVersion                   string
	MetricsRemoteWriteURL            string
	Replicas                         int
	Resources                        map[string]string
	NeutreeNodeAgentMetricsResources map[string]string
	KubeStateMetricsResources        map[string]string
	HashSuffix                       string
	EnableKubeStateMetrics           bool
	EnableNeutreeNodeAgentMetrics    bool
	EnableNodeExporter               bool
	AcceleratorExporters             []metricsAcceleratorExporter
	// ScrapeTargets are declarations selected by the caller, separate from workload runtime inputs.
	BaseScrapeTargets []metricsconfig.Target
	ScrapeTargets     []metricsconfig.Target
	EnableVMAgent     bool
	VMAgentConfig     string
	CacheScrapeConfig string

	// InferenceDefaultPort is the port the neutree-inference job scrapes when an
	// engine version does not declare a different one.
	InferenceDefaultPort int

	// InferenceScrapeRules carries the per-engine-version adjustments derived
	// from engine metrics-export declarations. Zero value means no engine
	// declared anything that differs from the defaults, and the job renders as
	// it did before engines could declare capabilities.
	InferenceScrapeRules InferenceScrapeRules
}

// buildManifestVariables creates the data structure for rendering manifests
func (m *MetricsComponent) buildManifestVariables() MetricsManifestVariables {
	// Default values for metrics component
	version := component.VictoriaMetrics
	replicas := 1
	resources := map[string]string{
		"cpu":    "100m",
		"memory": "256Mi",
	}
	kubeStateMetricsResources := map[string]string{
		"cpu":    "100m",
		"memory": "128Mi",
	}
	neutreeNodeAgentMetricsResources := map[string]string{
		"cpu":    "500m",
		"memory": "256Mi",
	}
	nodeAgentImage := "neutree/neutree-node-agent:" + component.LegacyNeutreeNodeAgent

	selection, err := clusterversion.SelectNodeAgent(m.cluster.GetVersion())

	if err == nil && selection.Image != "" {
		nodeAgentImage = selection.Image
	}

	return MetricsManifestVariables{
		ClusterName:                      m.cluster.Metadata.Name,
		Workspace:                        m.cluster.Metadata.Workspace,
		Namespace:                        m.namespace,
		ImagePullSecret:                  m.imagePullSecret,
		Version:                          version,
		VMAgentImage:                     util.RewriteImageRef(m.imagePrefix, defaultVMAgentImage),
		NodeExporterName:                 nodeExporterDaemonSetName,
		NodeExporterImage:                util.RewriteImageRef(m.imagePrefix, defaultNodeExporterImage),
		NodeExporterPort:                 nodeExporterPort,
		NeutreeNodeAgentMetricsName:      neutreeNodeAgentMetricsName,
		NeutreeNodeAgentMetricsImage:     util.RewriteImageRef(m.imagePrefix, nodeAgentImage),
		NeutreeNodeAgentMetricsPort:      neutreeNodeAgentMetricsPort,
		MetricsMode:                      string(m.cluster.Spec.Config.AcceleratorExporterMode()),
		NodeAgentClusterType:             v1.KubernetesClusterType,
		NodeAgentLegacyClusterType:       v1.KubernetesClusterType,
		NodeAgent:                        defaultMetricsNodeAgent(),
		KubeStateMetricsImage:            util.RewriteImageRef(m.imagePrefix, defaultKubeStateMetricsImage),
		ClusterVersion:                   m.cluster.GetVersion(),
		MetricsRemoteWriteURL:            m.metricsRemoteWriteURL,
		Replicas:                         replicas,
		Resources:                        resources,
		NeutreeNodeAgentMetricsResources: neutreeNodeAgentMetricsResources,
		KubeStateMetricsResources:        kubeStateMetricsResources,
		HashSuffix:                       util.HashString(m.cluster.Key()),
		InferenceDefaultPort:             v1.DefaultMetricsExportPort,
		InferenceScrapeRules:             m.inferenceScrapeRules,
	}
}

func (m *MetricsComponent) supportsKubeStateMetrics() (bool, error) {
	return clusterversion.SupportsKubeStateMetrics(m.cluster.GetVersion())
}

func (m *MetricsComponent) supportsManagedMetricsExporters() (bool, error) {
	return clusterversion.SupportsManagedMetricsExporters(m.cluster.GetVersion())
}
