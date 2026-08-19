// Package componentversion pins the versions of bundled infrastructure
// components (VictoriaMetrics, Grafana, Vector, Kong, Neutree node-agent) that
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

// NeutreeNodeAgent image version.
const NeutreeNodeAgent = "v1.1.0-rc.1"

// Grafana image version.
//
// Do not go below 12.1.0: the AppWrapper crash on boot over plain HTTP
// (`window.caches` is undefined outside a secure context) was fixed there and
// never backported to 11.5.x, 11.6.x or 12.0.x, and the 11.5 line is EOL.
//
// Grafana 12 removes AngularJS. Bundled dashboards still carrying `graph` /
// `singlestat` panels are core visualizations and are force-migrated on load,
// which is why their JSON is unchanged.
const Grafana = "12.4.9"

// Vector image version.
const Vector = "0.47.0-debian"

// Kong image version.
const Kong = "3.9"
