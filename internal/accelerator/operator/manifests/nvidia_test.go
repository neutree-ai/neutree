package manifests

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestRenderNvidiaProducesOperatorOwnedBaseResources(t *testing.T) {
	resources, err := RenderNvidia(context.Background(), NvidiaRenderOptions{
		Cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "cluster-a", Workspace: "workspace-a"},
			Spec:     &v1.ClusterSpec{Version: "v1.2.0"},
		},
		Namespace: "neutree-system",
		Profile: &v1.AcceleratorProfile{
			NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{Privileged: true},
			MetricsExporter: &v1.AcceleratorExporterProfile{
				Name:  "dcgm-exporter",
				Image: "example/dcgm-exporter:v1",
				Port:  19400,
			},
		},
		Managed: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	expected := map[string]string{
		"ServiceAccount/neutree-node-agent":                "v1",
		"ClusterRole/neutree-node-agent-ed5437dc6f":        "rbac.authorization.k8s.io/v1",
		"ClusterRoleBinding/neutree-node-agent-ed5437dc6f": "rbac.authorization.k8s.io/v1",
		"DaemonSet/neutree-node-agent":                     "apps/v1",
		"DaemonSet/nvidia-gpu-dcgm-exporter":               "apps/v1",
	}
	if len(resources.Items) != len(expected) {
		t.Fatalf("resources=%d, want %d base resources", len(resources.Items), len(expected))
	}
	for _, object := range resources.Items {
		key := object.GetKind() + "/" + object.GetName()
		apiVersion, found := expected[key]
		if !found || object.GetAPIVersion() != apiVersion {
			t.Errorf("unexpected rendered resource: kind=%q apiVersion=%q name=%q", object.GetKind(), object.GetAPIVersion(), object.GetName())
			continue
		}
		delete(expected, key)

		if object.GetKind() == "DaemonSet" && object.GetName() != "" {
			spec, found, err := unstructured.NestedSlice(object.Object, "spec", "template", "spec", "initContainers")
			if err != nil || !found || len(spec) == 0 {
				t.Fatalf("DaemonSet %s missing toolkit-ready init container", object.GetName())
			}
			liveDriverGate := false
			for _, raw := range spec {
				container, ok := raw.(map[string]interface{})
				if !ok || container["name"] != "toolkit-validation" {
					continue
				}
				args, _, err := unstructured.NestedStringSlice(container, "args")
				if err == nil && strings.Contains(strings.Join(args, " "), "grep -q '^nvidia ' /proc/modules") {
					liveDriverGate = true
				}
			}
			if !liveDriverGate {
				t.Fatalf("DaemonSet %s missing live-driver toolkit gate", object.GetName())
			}
		}
	}
	if len(expected) != 0 {
		t.Errorf("missing rendered resources: %v", expected)
	}
}
