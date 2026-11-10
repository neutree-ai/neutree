package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNvidiaKubernetesRuntimeDefaultsAndToolkitInjection(t *testing.T) {
	for _, test := range []struct {
		name        string
		policies    []GPUOperatorClusterPolicy
		wantManaged bool
		wantRuntime string
	}{
		{name: "managed default runtime", policies: []GPUOperatorClusterPolicy{{Spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}}}}, wantManaged: true, wantRuntime: "nvidia"},
		{name: "managed configured runtime", policies: []GPUOperatorClusterPolicy{{Spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}, "operator": map[string]interface{}{"runtimeClass": "custom-nvidia"}}}}, wantManaged: true, wantRuntime: "custom-nvidia"},
		{name: "driver only", policies: []GPUOperatorClusterPolicy{{Spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": false}}}}, wantManaged: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			managed, runtimeClass := nvidiaKubernetesRuntime(test.policies)
			assert.Equal(t, test.wantManaged, managed)
			assert.Equal(t, test.wantRuntime, runtimeClass)

			node := &v1.NodeAgentRuntimeProfile{Runtime: "nvidia"}
			init := []corev1.Container{}
			volumes := []v1.ComponentVolume{}
			mounts := []v1.ComponentVolumeMount{}
			if managed {
				appendToolkitValidation(&init, &volumes, &mounts)
			}
			resolved := enrichNodeAgentRuntime(&v1.NodeAgentRuntimeProfile{Runtime: node.Runtime}, true, runtimeClass, managed)
			if managed {
				require.Len(t, resolved.InitContainers, 1)
				assert.Equal(t, "toolkit-validation", resolved.InitContainers[0].Name)
				assert.Contains(t, resolved.InitContainers[0].Args[0], "/run/nvidia/validations/toolkit-ready")
				assert.NotEmpty(t, resolved.Volumes)
				assert.NotEmpty(t, resolved.VolumeMounts)
				assert.Equal(t, v1.ComponentHostPathTypeDirectoryOrCreate, resolved.Volumes[0].HostPath.Type)
			} else {
				assert.Empty(t, resolved.InitContainers)
			}
		})
	}
}

func TestNvidiaKubernetesProfileResolverIsOptionalForStaticProfiles(t *testing.T) {
	profile := &v1.AcceleratorProfile{NodeAgentRuntime: &v1.NodeAgentRuntimeProfile{Runtime: "nvidia"}}
	resolved, err := (&GPUAcceleratorPlugin{}).ResolveKubernetesProfile(context.Background(), nil, profile)
	require.NoError(t, err)
	assert.Same(t, profile, resolved)
}
