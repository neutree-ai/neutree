package operator

import (
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// UserVirtualizationConfigPatch keeps the public patch surface limited to
// scheduler/global values. Device-plugin values belong to the accelerator
// provider and must not be replaced by a cluster request.
func UserVirtualizationConfigPatch(configPatch map[string]interface{}) map[string]interface{} {
	if len(configPatch) == 0 {
		return nil
	}

	copy := map[string]interface{}{}
	data, err := yaml.Marshal(configPatch)
	if err != nil || yaml.Unmarshal(data, &copy) != nil {
		return nil
	}

	delete(copy, "devicePlugin")
	unstructured.RemoveNestedField(copy, "scheduler", "admissionWebhook", "namespaceSelector")

	return copy
}
