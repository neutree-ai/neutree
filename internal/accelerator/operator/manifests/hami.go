package manifests

import (
	"bytes"
	"embed"
	"path"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/neutree-ai/neutree/internal/util"
)

//go:embed chart/control-plane.tgz chart/device-plugin.tgz
var embeddedHamiCharts embed.FS

func RenderHami(
	namespace string,
	imagePrefix string,
	configPatch map[string]interface{},
	devicePluginEnabled bool,
) (*unstructured.UnstructuredList, error) {
	archive := "chart/control-plane.tgz"
	if devicePluginEnabled {
		archive = "chart/device-plugin.tgz"
	}

	chartData, err := embeddedHamiCharts.ReadFile(archive)
	if err != nil {
		return nil, errors.Wrap(err, "read embedded Hami chart")
	}

	hamiChart, err := loader.LoadArchive(bytes.NewReader(chartData))
	if err != nil {
		return nil, errors.Wrap(err, "load embedded Hami chart")
	}

	values := map[string]interface{}{
		"fullnameOverride": "hami",
		"global": map[string]interface{}{
			"imageRegistry": strings.TrimRight(imagePrefix, "/"),
			"imageTag":      "v2.10.0",
		},
		"devicePlugin": map[string]interface{}{
			"enabled": devicePluginEnabled,
		},
	}
	mergeValues(values, configPatch)

	devicePluginValues, ok := values["devicePlugin"].(map[string]interface{})
	if !ok {
		return nil, errors.New("devicePlugin values must be a map")
	}

	devicePluginValues["enabled"] = devicePluginEnabled

	capabilities := chartutil.DefaultCapabilities.Copy()

	renderValues, err := chartutil.ToRenderValuesWithSchemaValidation(
		hamiChart,
		values,
		chartutil.ReleaseOptions{Name: "hami", Namespace: namespace, Revision: 1, IsInstall: true},
		capabilities,
		true,
	)
	if err != nil {
		return nil, errors.Wrap(err, "build Hami render values")
	}

	files, err := engine.Engine{}.Render(hamiChart, renderValues)
	if err != nil {
		return nil, errors.Wrap(err, "render Hami chart")
	}

	manifest := joinYAML(files)

	resources, err := util.DecodeKubernetesManifest(manifest)
	if err != nil {
		return nil, errors.Wrap(err, "decode Hami manifests")
	}

	return resources, nil
}

func RenderHamiDevicePlugin(namespace, imagePrefix string, configPatch map[string]interface{}, toolkitReady bool) (*unstructured.UnstructuredList, error) {
	patch := cloneValues(configPatch)
	if toolkitReady {
		mergeValues(patch, map[string]interface{}{
			"devicePlugin": map[string]interface{}{
				"gpuOperatorToolkitReady": map[string]interface{}{
					"enabled":  true,
					"hostPath": "/run/nvidia/validations",
				},
			},
		})
	}

	return RenderHami(namespace, imagePrefix, patch, true)
}

func cloneValues(values map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{}

	for key, value := range values {
		nested, ok := value.(map[string]interface{})
		if ok {
			result[key] = cloneValues(nested)
			continue
		}

		result[key] = value
	}

	return result
}

func mergeValues(base, patch map[string]interface{}) {
	for key, value := range patch {
		nested, ok := value.(map[string]interface{})
		if !ok {
			base[key] = value
			continue
		}

		existing, _ := base[key].(map[string]interface{})
		if existing == nil {
			existing = map[string]interface{}{}
			base[key] = existing
		}

		mergeValues(existing, nested)
	}
}

func joinYAML(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	var out strings.Builder

	for _, key := range keys {
		if path.Ext(key) != ".yaml" && path.Ext(key) != ".yml" {
			continue
		}

		content := strings.TrimSpace(files[key])
		if content == "" {
			continue
		}

		out.WriteString("---\n")
		out.WriteString(content)
		out.WriteString("\n")
	}

	return out.String()
}
