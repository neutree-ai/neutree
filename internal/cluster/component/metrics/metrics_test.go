package metrics

import "testing"

func TestBuildVMAgentDeployment(t *testing.T) {
	metricsCmpt := &MetricsComponent{
		clusterName:     "test-cluster",
		workspace:       "test-workspace",
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	deployment, err := metricsCmpt.buildVMAgentDeployment()
	if err != nil {
		t.Fatalf("Failed to build vmagent deployment: %v", err)
	}

	if deployment.GetName() != "vmagent" {
		t.Errorf("Expected deployment name 'vmagent', got '%s'", deployment.GetName())
	}

	if deployment.GetLabels()["app"] != "vmagent" {
		t.Errorf("Expected label 'app=vmagent', got '%s'", deployment.GetLabels()["app"])
	}
}
