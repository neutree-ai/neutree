// Package componentversion pins the versions of bundled third-party
// infrastructure components (VictoriaMetrics, Grafana, Vector, Kong) that
// Neutree deploys for both compose-based control-plane installs and the
// in-cluster metrics stack. The constants live in internal/ so they can be
// shared between cmd/neutree-cli (deployer) and internal/cluster/component
// (cluster-side manifest generator) without forcing internal/ to import cmd/.
package componentversion

// VictoriaMetrics image versions.
const (
	VictoriaMetrics        = "v1.115.0"
	VictoriaMetricsCluster = VictoriaMetrics + "-cluster"
)

// KubeStateMetrics image version.
const KubeStateMetrics = "v2.15.0"

// NodeExporter image version.
const NodeExporter = "v1.8.2"

// Grafana image version.
const Grafana = "11.5.3"

// Vector image version.
const Vector = "0.47.0-debian"

// Kong image version.
const Kong = "3.9"
