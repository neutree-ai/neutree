package metrics

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/deploy"
)

func TestMetricsDeleteCleansOnlyBoundLegacyAndGenericNodes(t *testing.T) {
	for _, foreignLegacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed owners", true: "foreign legacy workload"}[foreignLegacy], func(t *testing.T) {
			profiles := &acceleratormocks.MockManager{}
			profiles.On("SupportPlugins").Return([]string{"legacy_gpu"})
			profiles.On("GetAcceleratorProfile", mock.Anything, "legacy_gpu").Return(&v1.AcceleratorProfile{
				MetricsExporter: &v1.AcceleratorExporterProfile{
					Name: "exporter", Image: "vendor/exporter:test", Port: 19500,
					Runtime: &v1.AcceleratorExporterRuntimeProfile{NodeSelector: map[string]string{"vendor/gpu": "true"}},
				},
			}, nil)
			node := func(name string, labels map[string]string) *corev1.Node {
				return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Annotations: map[string]string{
					resourceparser.NeutreeAcceleratorDevicesAnnotation: `[{"uuid":"GPU-` + name + `"}]`,
					"example.com/keep": "value",
				}}}
			}
			ctrClient := fake.NewClientBuilder().WithObjects(
				node("operator-node", map[string]string{"nvidia.com/gpu.present": "true"}),
				node("legacy-node", map[string]string{"vendor/gpu": "true"}),
			).Build()
			component := operatorMetricsComponent(ctrClient)
			component.acceleratorMgr = profiles
			plan, err := component.deploymentPlan(context.Background())
			require.NoError(t, err)
			var inventory []*appsv1.DaemonSet
			for _, name := range plan.nodeAgentNames() {
				labels := component.resourceLabels()
				labels["app"] = name
				labels["neutree.ai/metrics-target"] = "node-agent"
				nodeName := "cpu-node"
				if name == plan.LegacyNodeAgent {
					nodeName = "legacy-node"
					if foreignLegacy {
						labels[v1.NeutreeClusterWorkspaceLabelKey] = "foreign"
					}
				}
				ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: component.namespace, UID: types.UID(name), Labels: labels}}
				ds.APIVersion, ds.Kind = "apps/v1", "DaemonSet"
				require.NoError(t, ctrClient.Create(context.Background(), ds))
				if !foreignLegacy || name != plan.LegacyNodeAgent {
					inventory = append(inventory, ds)
				}
				controller := true
				require.NoError(t, ctrClient.Create(context.Background(), &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: name + "-pod", Namespace: component.namespace, Labels: labels,
						OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", Name: name, UID: ds.UID, Controller: &controller}}},
					Spec: corev1.PodSpec{NodeName: nodeName},
				}))
			}
			require.NoError(t, ctrClient.Create(context.Background(), &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "operator-node-agent", Namespace: component.namespace,
					Labels: map[string]string{"neutree.ai/metrics-target": "node-agent", "neutree.ai/accelerator-operator": "nvidia"}},
				Spec: corev1.PodSpec{NodeName: "operator-node"},
			}))
			data, err := json.Marshal(inventory)
			require.NoError(t, err)
			require.NoError(t, deploy.NewConfigStore(ctrClient).Set(context.Background(), component.namespace, component.cluster.GetName(), "metrics", string(data), nil))
			if foreignLegacy {
				require.NoError(t, component.Delete())
			} else {
				require.ErrorContains(t, component.Delete(), "metrics resources are not fully deleted")
			}
			for _, name := range []string{"operator-node", "legacy-node"} {
				got := &corev1.Node{}
				require.NoError(t, ctrClient.Get(context.Background(), client.ObjectKey{Name: name}, got))
				_, exists := got.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]
				require.Equal(t, name == "operator-node" || (foreignLegacy && name == "legacy-node"), exists, name)
				require.Equal(t, "value", got.Annotations["example.com/keep"])
			}
			// The first call deleted the owned DaemonSets after capturing bindings;
			// the second call completes without broadening cleanup to unknown nodes.
			require.NoError(t, component.Delete())
		})
	}
}
