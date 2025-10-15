package orchestrator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildDeployment(t *testing.T) {
	data := DeploymentManifestData{
		ClusterName:     "test-cluster",
		Workspace:       "test-workspace",
		Namespace:       "default",
		ImagePrefix:     "myrepo",
		ImagePullSecret: "my-secret",
		EngineName:      "test-engine",
		EngineVersion:   "v1.0.0",
		EndpointName:    "test-endpoint",
		ModelArgs: map[string]interface{}{
			"name": "gpt-4",
			"task": "text-generation",
		},
		EngineArgs: map[string]interface{}{
			"max-concurrency": "10",
			"timeout":         "60s",
		},
		Resources: map[string]string{
			"cpu":    "500m",
			"memory": "1Gi",
		},
		RoutingLogic: "roundrobin",
		Replicas:     2,
		Volumes: []corev1.Volume{
			{
				Name: "model-volume",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{
						Path: "/mnt/model",
					},
				},
			},
		},

		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "model-volume",
				MountPath: "/mnt/model",
			},
		},
	}

	obj, err := buildDeployment(data)
	if err != nil {
		t.Fatalf("Failed to build deployment: %v", err)
	}

	if obj.GetName() != "test-endpoint" {
		t.Errorf("Expected deployment name 'test-endpoint', got '%s'", obj.GetName())
	}

	// Additional checks can be added here to validate the structure of the generated object
}
