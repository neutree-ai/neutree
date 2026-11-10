package manifests

import (
	_ "embed"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/neutree-ai/neutree/internal/util"
)

//go:embed nvidia_device_plugin.yaml
var nvidiaDevicePluginTemplate string

type NvidiaDevicePluginRenderOptions struct {
	Namespace       string
	ImagePrefix     string
	ImagePullSecret string
	Version         string
	NodeSelector    map[string]string
	DriverRoot      string
	Managed         bool
}

func RenderNvidiaDevicePlugin(options NvidiaDevicePluginRenderOptions) (*unstructured.UnstructuredList, error) {
	imagePrefix := strings.TrimRight(strings.TrimSpace(options.ImagePrefix), "/")
	if util.IsDockerHubImagePrefix(imagePrefix) {
		imagePrefix = ""
	}

	resources, err := util.RenderKubernetesManifest(nvidiaDevicePluginTemplate, struct {
		Namespace, ImagePrefix, ImagePullSecret, Version, DriverRoot string
		NodeSelector                                                 map[string]string
	}{
		Namespace: options.Namespace, ImagePrefix: imagePrefix, ImagePullSecret: options.ImagePullSecret,
		Version: options.Version, DriverRoot: options.DriverRoot, NodeSelector: options.NodeSelector,
	})
	if err != nil {
		return nil, err
	}

	if err := addToolkitValidation(resources, options.Managed); err != nil {
		return nil, err
	}

	return resources, nil
}

func NvidiaDevicePluginConfig(patch map[string]interface{}) (map[string]string, string, error) {
	devicePlugin, ok := nestedConfigMap(patch, "devicePlugin")
	if !ok {
		return nil, "", fmt.Errorf("NVIDIA devicePlugin config is missing")
	}

	selector := map[string]string{}
	if raw, exists := devicePlugin["nvidiaNodeSelector"]; exists {
		values, ok := raw.(map[string]interface{})
		if !ok {
			return nil, "", fmt.Errorf("NVIDIA nvidiaNodeSelector config must be a map")
		}

		selector = stringConfigMap(values)
	}

	driverRoot, _ := devicePlugin["nvidiaDriverRoot"].(string)

	return selector, driverRoot, nil
}

func nestedConfigMap(values map[string]interface{}, key string) (map[string]interface{}, bool) {
	value, ok := values[key].(map[string]interface{})
	return value, ok
}

func stringConfigMap(values map[string]interface{}) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		if text, ok := value.(string); ok && text != "" {
			result[key] = text
		}
	}
	return result
}
