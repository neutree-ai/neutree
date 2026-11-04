package orchestrator

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/kvcache"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type cacheUpgradeGate struct {
	kvcache.VLLMNative
	ready    bool
	err      error
	observed bool
}

func (g *cacheUpgradeGate) DeploymentReady(*v1.Endpoint, *v1.Cluster) (bool, error) {
	return g.ready, g.err
}
func (g *cacheUpgradeGate) ObserveDeployment(_ *v1.Endpoint, _ *v1.Cluster, _ *appsv1.Deployment, status *v1.EndpointStatus) error {
	g.observed = true
	status.KVCache = &v1.EndpointKVCacheStatus{Provider: "shared"}
	return nil
}

func TestCacheUpgradeGateLeavesDeploymentUntouched(t *testing.T) {
	for _, failure := range []error{nil, errors.New("cache unavailable")} {
		gate := &cacheUpgradeGate{err: failure}
		o := &kubernetesOrchestrator{kvCacheProviders: kvcache.Providers{"shared": gate}}
		ep := &v1.Endpoint{Spec: &v1.EndpointSpec{KVCache: &v1.EndpointKVCacheSpec{Provider: "shared"}}}
		// No Kubernetes client or rendering inputs: a closed gate must stop before
		// creating or replacing the existing inference Deployment.
		err := o.createEndpoint(&OrchestratorContext{Endpoint: ep})
		require.ErrorIs(t, err, failure)
	}
}

func TestCacheUpgradeStatusFences(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ready    bool
		gateErr  error
		paused   bool
		observed int64
		hash     string
		deadline bool
		phase    v1.EndpointPhase
		message  string
	}{
		{name: "runtime upgrading", phase: v1.EndpointPhaseDEPLOYING, message: "Waiting for cache runtime upgrade"},
		{name: "provider error", gateErr: errors.New("cache unavailable")},
		{name: "pause bypasses runtime gate", paused: true, phase: v1.EndpointPhasePAUSED},
		{name: "stale endpoint template", ready: true, hash: "old-spec", phase: v1.EndpointPhaseDEPLOYING, message: "Waiting for the current endpoint configuration"},
		{name: "deployment generation not observed", ready: true, phase: v1.EndpointPhaseDEPLOYING, message: "Waiting for deployment observation"},
		{name: "rollout failure", ready: true, observed: 2, deadline: true, phase: v1.EndpointPhaseFAILED, message: "image pull failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replicas := 1
			if tc.paused {
				replicas = 0
			}
			ep := &v1.Endpoint{Metadata: &v1.Metadata{Name: "ep"}, Spec: &v1.EndpointSpec{Replicas: v1.ReplicaSpec{Num: &replicas}, KVCache: &v1.EndpointKVCacheSpec{Provider: "shared"}}}
			gate := &cacheUpgradeGate{ready: tc.ready, err: tc.gateErr}
			o := &kubernetesOrchestrator{kvCacheProviders: kvcache.Providers{"shared": gate}}
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "ep", Namespace: "test-namespace", Generation: 2, Annotations: map[string]string{annEndpointSpecHash: tc.hash}}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"endpoint": "ep"}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: tc.observed}}
			if tc.deadline {
				dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: tc.message}}
			}
			c := NewFakeK8sClient(t)
			require.NoError(t, c.Create(context.Background(), dep))
			status, err := o.getEndpointStats(c, "test-namespace", &v1.Cluster{}, ep)
			if tc.gateErr != nil {
				require.ErrorIs(t, err, tc.gateErr)
				require.False(t, gate.observed)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.phase, status.Phase)
			require.Equal(t, tc.message, status.ErrorMessage)
			require.True(t, gate.observed)
			require.Equal(t, "shared", status.KVCache.Provider)
		})
	}
}
