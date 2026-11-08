// Package manifests contains metrics deployment and scrape template text.
package manifests

const Template = vmagentPrerequisitesManifestTemplate + stateMetricsDeploymentManifestTemplate +
	nodeAgentRBACManifestTemplate + nodeExporterDeploymentManifestTemplate +
	nodeAgentDeploymentManifestTemplate + acceleratorExporterDeploymentManifestTemplate + vmagentDeploymentManifestTemplate
