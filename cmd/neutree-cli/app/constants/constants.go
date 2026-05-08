// Package constants holds CLI-only deployment-shape strings used by
// neutree-cli launch. Cross-cutting infrastructure facts that the cluster
// side also needs (component image versions) live in
// internal/componentversion to keep cluster-side packages from depending
// upward into cmd/.
package constants

// deploy type
const (
	DeployTypeLocal = "local"
)

// deploy mode
const (
	DeployModeCluster = "cluster"
	DeployModeSingle  = "single"
)
