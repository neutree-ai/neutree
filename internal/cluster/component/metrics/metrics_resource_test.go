package metrics

import (
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
)

func TestBuildVMAgentDeployment(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := metricsCmpt.GetMetricsResources()
	if err != nil {
		t.Fatalf("Failed to build vmagent deployment: %v", err)
	}

	for _, obj := range objs.Items {
		if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" && obj.GetName() == "vmagent" {
			objContent, _ := json.Marshal(obj.Object)
			deployment := &appsv1.Deployment{}
			err = json.Unmarshal(objContent, deployment)
			if err != nil {
				t.Fatalf("Failed to unmarshal deployment: %v", err)
			}

			assert.Equal(t, "test-namespace", deployment.GetNamespace(), "Deployment namespace mismatch")
			assert.Equal(t, "vmagent", deployment.GetName(), "Deployment name mismatch")
			assert.Equal(t, "vmagent", deployment.GetLabels()["app"], "Deployment app label mismatch")
			assert.Equal(t, int32(1), *deployment.Spec.Replicas, "Deployment replicas mismatch")
			assert.Equal(t, "test-image-prefix/vmagent:v1.115.0", deployment.Spec.Template.Spec.Containers[0].Image, "Deployment image mismatch")
			assert.Equal(t, "test-image-pull-secret", deployment.Spec.Template.Spec.ImagePullSecrets[0].Name, "Deployment image pull secret mismatch")
			return
		}
	}

	t.Fatalf("vmagent deployment not found in resources")
}
