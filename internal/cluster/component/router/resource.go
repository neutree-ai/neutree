package router

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// renderManifest renders a manifest template with the provided data
func (r *RouterComponent) renderManifest(templateStr string, data RouteManifestData) (client.Object, error) {
	tmpl, err := template.New("manifest").Parse(templateStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse template")
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, errors.Wrap(err, "failed to execute template")
	}

	fmt.Println(buf.String())
	// Decode YAML to unstructured object
	obj := &unstructured.Unstructured{}
	decoder := yaml.NewYAMLOrJSONDecoder(&buf, 4096)
	if err := decoder.Decode(obj); err != nil {
		return nil, errors.Wrap(err, "failed to decode manifest")
	}

	return obj, nil
}

// buildManifestData creates the data structure for rendering manifests
func (r *RouterComponent) buildManifestData() RouteManifestData {
	return RouteManifestData{
		ClusterName:     r.clusterName,
		Workspace:       r.workspace,
		Namespace:       r.namespace,
		ImagePrefix:     r.imagePrefix,
		ImagePullSecret: r.imagePullSecret,
		Version:         r.config.Route.Version,
		Replicas:        r.config.Route.Replicas,
		Resources:       r.config.Route.Resources,
	}
}

// buildRouteService builds the router service from manifest template
func (r *RouterComponent) buildRouteService() (client.Object, error) {
	data := r.buildManifestData()
	return r.renderManifest(routerServiceTemplate, data)
}

// buildRouterDeployment builds the router deployment from manifest template
func (r *RouterComponent) buildRouterDeployment() (client.Object, error) {
	data := r.buildManifestData()
	return r.renderManifest(routerDeploymentTemplate, data)
}

// buildRouterServiceAccount builds the router service account from manifest template
func (r *RouterComponent) buildRouterServiceAccount() (client.Object, error) {
	data := r.buildManifestData()
	return r.renderManifest(routerServiceAccountTemplate, data)
}

// buildRouterRole builds the router role from manifest template
func (r *RouterComponent) buildRouterRole() (client.Object, error) {
	data := r.buildManifestData()
	return r.renderManifest(routerRoleTemplate, data)
}

// buildRouterRoleBinding builds the router role binding from manifest template
func (r *RouterComponent) buildRouterRoleBinding() (client.Object, error) {
	data := r.buildManifestData()
	return r.renderManifest(routerRoleBindingTemplate, data)
}
