package accelerator

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNvidiaOperatorManagementRequiresBothFlags(t *testing.T) {
	for _, tc := range []struct {
		name        string
		spec        map[string]interface{}
		wantManaged bool
	}{
		{name: "explicit dual management", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": true}}, wantManaged: true},
		{name: "driver only", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}, "toolkit": map[string]interface{}{"enabled": false}}},
		{name: "toolkit only", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": false}, "toolkit": map[string]interface{}{"enabled": true}}},
		{name: "missing management flag", spec: map[string]interface{}{"driver": map[string]interface{}{"enabled": true}}},
		{name: "empty policy", spec: map[string]interface{}{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			managed, _, err := NvidiaOperatorRuntime([]GPUOperatorClusterPolicy{{Name: "policy", Spec: tc.spec}})
			require.NoError(t, err)
			require.Equal(t, tc.wantManaged, managed)
		})
	}
}

func TestNvidiaNodesRuntimeScope(t *testing.T) {
	gpu := func(name, runtime string) corev1.Node {
		return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{NvidiaGPUDiscoveryLabelKey: "true"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{ContainerRuntimeVersion: runtime}}}
	}
	for _, tc := range []struct {
		name  string
		nodes []corev1.Node
		want  bool
	}{
		{name: "containerd GPU with other CPU runtime", nodes: []corev1.Node{gpu("gpu", "containerd://1.7.28"), {ObjectMeta: metav1.ObjectMeta{Name: "cpu"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{ContainerRuntimeVersion: "cri-o://1.29"}}}}, want: true},
		{name: "mixed GPU runtimes", nodes: []corev1.Node{gpu("gpu1", "containerd://1.7.28"), gpu("gpu2", "cri-o://1.29")}},
		{name: "runtime not yet reported", nodes: []corev1.Node{gpu("gpu", "")}},
		{name: "no discovered GPU"},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, NvidiaNodesUseContainerd(tc.nodes)) })
	}
}
