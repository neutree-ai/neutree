package metrics

import (
	"bytes"
	"text/template"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/constants"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// renderManifest renders a manifest template with the provided data
func (m *MetricsComponent) renderManifest(templateStr string, data MetricsManifestData) (client.Object, error) {
	tmpl, err := template.New("manifest").Parse(templateStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse template")
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, errors.Wrap(err, "failed to execute template")
	}

	// Decode YAML to unstructured object
	obj := &unstructured.Unstructured{}
	decoder := yaml.NewYAMLOrJSONDecoder(&buf, 4096)
	if err := decoder.Decode(obj); err != nil {
		return nil, errors.Wrap(err, "failed to decode manifest")
	}

	return obj, nil
}

// buildManifestData creates the data structure for rendering manifests
func (m *MetricsComponent) buildManifestData() MetricsManifestData {
	// Default values for metrics component
	version := constants.VictoriaMetricsVersion
	replicas := 1
	resources := map[string]string{
		"cpu":    "100m",
		"memory": "256Mi",
	}

	return MetricsManifestData{
		ClusterName:           m.clusterName,
		Workspace:             m.workspace,
		Namespace:             m.namespace,
		ImagePrefix:           m.imagePrefix,
		ImagePullSecret:       m.imagePullSecret,
		Version:               version,
		MetricsRemoteWriteURL: m.metricsRemoteWriteURL,
		Replicas:              replicas,
		Resources:             resources,
	}
}

// buildVMAgentConfigMap builds the vmagent config ConfigMap from manifest template
func (m *MetricsComponent) buildVMAgentConfigMap() (client.Object, error) {
	data := m.buildManifestData()
	return m.renderManifest(vmAgentConfigMapTemplate, data)
}

// buildVMAgentScrapeConfigMap builds the vmagent scrape config ConfigMap from manifest template
func (m *MetricsComponent) buildVMAgentScrapeConfigMap() (client.Object, error) {
	data := m.buildManifestData()
	return m.renderManifest(vmAgentScrapeConfigMapTemplate, data)
}

// buildVMAgentDeployment builds the vmagent deployment from manifest template
func (m *MetricsComponent) buildVMAgentDeployment() (client.Object, error) {
	data := m.buildManifestData()
	return m.renderManifest(vmAgentDeploymentTemplate, data)
}

// buildVMAgentServiceAccount builds the vmagent service account from manifest template
func (m *MetricsComponent) buildVMAgentServiceAccount() (client.Object, error) {
	data := m.buildManifestData()
	return m.renderManifest(vmAgentServiceAccountTemplate, data)
}

// buildVMAgentRole builds the vmagent role from manifest template
func (m *MetricsComponent) buildVMAgentRole() (client.Object, error) {
	data := m.buildManifestData()
	return m.renderManifest(vmAgentRoleTemplate, data)
}

// buildVMAgentRoleBinding builds the vmagent role binding from manifest template
func (m *MetricsComponent) buildVMAgentRoleBinding() (client.Object, error) {
	data := m.buildManifestData()
	return m.renderManifest(vmAgentRoleBindingTemplate, data)
}
