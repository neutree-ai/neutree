// Package component defines release versions for bundled
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

// NeutreeNodeAgentV120 is the NodeAgent image pinned for clusters from v1.1.2
// through v1.2.0. Those clusters predate version-aware image selection, so they
// keep the image they were released with.
const NeutreeNodeAgentV120 = "v1.2.0-rc.1"

// NeutreeNodeAgentV121 is the NodeAgent image pinned for clusters from v1.2.1
// up to, but excluding, v1.3.0.
const NeutreeNodeAgentV121 = "v1.2.1-rc.1"

// NeutreeNodeAgentV130 is the image pin for the v1.3.0 cluster release cycle.
// It can change during development and is frozen once v1.3.0 is released.
// Later release cycles must use new versioned constants.
const NeutreeNodeAgentV130 = "v1.3.0-rc.1"

// Grafana image version.
const Grafana = "11.5.3"

// Vector image version.
const Vector = "0.47.0-debian"

// Kong image version.
const Kong = "3.9"

const (
	HAMi     = "v2.9.0"
	V130HAMI = "v2.10.0"
)
