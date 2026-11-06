package operator

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestOperatorResourceLabelsReachDaemonSetPodTemplate(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "DaemonSet",
		"metadata": map[string]interface{}{
			"name":      "nvidia-dcgm-exporter",
			"namespace": "neutree-system",
		},
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"app": "nvidia-dcgm-exporter",
				},
			},
		},
	}}
	cluster := &v1.Cluster{Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"}}

	OperatorResourceLabels(object, cluster)

	labels, found, err := unstructured.NestedStringMap(object.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found {
		t.Fatalf("template labels missing: found=%v err=%v", found, err)
	}
	if labels[OperatorOwnerLabel] != OperatorOwnerValue {
		t.Fatalf("operator owner label missing: %#v", labels)
	}
	if labels[v1.NeutreeClusterLabelKey] != "cluster-a" || labels[v1.NeutreeClusterWorkspaceLabelKey] != "workspace-a" {
		t.Fatalf("cluster labels missing: %#v", labels)
	}
}
