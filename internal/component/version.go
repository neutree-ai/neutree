// Package component defines versions and compatibility rules for bundled
// infrastructure components shared by deployment and cluster renderers.
package component

// VictoriaMetrics image versions.
const (
	VictoriaMetrics        = "v1.115.0"
	VictoriaMetricsCluster = VictoriaMetrics + "-cluster"
)

// KubeStateMetrics image version.
const KubeStateMetrics = "v2.15.0"

// NodeExporter image version.
const NodeExporter = "v1.8.2"

// LegacyNeutreeNodeAgent is the last NodeAgent image that still uses the
// legacy CLI contract rendered for clusters up to v1.1.1.
const LegacyNeutreeNodeAgent = "v1.1.0-rc.1"

// NeutreeNodeAgent is the profile-selected NodeAgent image for clusters newer
// than v1.1.1.
const NeutreeNodeAgent = "v1.2.0-rc.1"

// Grafana image version.
const Grafana = "11.5.3"

// Vector image version.
const Vector = "0.47.0-debian"

// Kong image version.
const Kong = "3.9"
