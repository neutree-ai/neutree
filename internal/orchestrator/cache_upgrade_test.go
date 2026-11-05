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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type cacheUpgradeGate struct {
	kvcache.VLLMNative
	ready       bool
	err         error
	observed    bool
	dep         *appsv1.Deployment
	specPending bool
	observeErr  error
}

func (g *cacheUpgradeGate) DeploymentReady(*v1.Endpoint, *v1.Cluster) (bool, error) {
	return g.ready, g.err
}
func (g *cacheUpgradeGate) ObserveStatus(_ context.Context, _ client.Client, _ string, _ *v1.Endpoint, _ *v1.Cluster, dep *appsv1.Deployment, specPending bool, status *v1.EndpointStatus) error {
	g.observed = true
	g.dep = dep
	g.specPending = specPending
	status.KVCache = &v1.EndpointKVCacheStatus{Provider: "shared"}
	return g.observeErr
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
		name                                                                              string
		paused, missing, deleting, lingering, unregistered, pending, unobserved, deadline bool
		observeErr                                                                        error
		phase                                                                             v1.EndpointPhase
		message                                                                           string
	}{
		{name: "closed gate does not change healthy status", phase: v1.EndpointPhaseRUNNING},
		{name: "pause bypasses gate", paused: true, phase: v1.EndpointPhasePAUSED},
		{name: "paused without deployment", paused: true, missing: true, phase: v1.EndpointPhasePAUSED},
		{name: "stale endpoint template", pending: true, phase: v1.EndpointPhaseDEPLOYING, message: "Waiting for the current endpoint configuration"},
		{name: "deployment generation not observed", unobserved: true, phase: v1.EndpointPhaseDEPLOYING, message: "Waiting for deployment observation"},
		{name: "rollout failure with closed gate", deadline: true, phase: v1.EndpointPhaseFAILED, message: "image pull failed"},
		{name: "missing deployment still observes dependencies", missing: true, phase: v1.EndpointPhaseDEPLOYING, message: "Endpoint deploying in progress: Endpoint deployment not found"},
		{name: "deleting still observes remaining pods", missing: true, deleting: true, lingering: true, phase: v1.EndpointPhaseDELETING, message: "waiting for 1 pod(s)"},
		{name: "deleted endpoint ignores historical provider", missing: true, deleting: true, unregistered: true, phase: v1.EndpointPhaseDELETED},
		{name: "observation error is propagated", observeErr: errors.New("pod observation unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replicas := 1
			if tc.paused {
				replicas = 0
			}
			ep := &v1.Endpoint{Metadata: &v1.Metadata{Name: "ep"}, Spec: &v1.EndpointSpec{Replicas: v1.ReplicaSpec{Num: &replicas}, KVCache: &v1.EndpointKVCacheSpec{Provider: "shared"}}, Status: &v1.EndpointStatus{KVCache: &v1.EndpointKVCacheStatus{Provider: "shared"}}}
			if tc.deleting {
				ep.Metadata.DeletionTimestamp = "2026-11-05T00:00:00Z"
			}
			gate := &cacheUpgradeGate{err: errors.New("gate must not be called while reading status"), observeErr: tc.observeErr}
			o := &kubernetesOrchestrator{kvCacheProviders: kvcache.Providers{"shared": gate}}
			if tc.unregistered {
				o.kvCacheProviders = nil
			}
			hash, err := computeEndpointSpecHash(ep)
			require.NoError(t, err)
			if tc.pending {
				hash = "previous"
			}
			n := int32(1)
			dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "ep", Namespace: "test-namespace", Generation: 2, Annotations: map[string]string{annEndpointSpecHash: hash}}, Spec: appsv1.DeploymentSpec{Replicas: &n, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"endpoint": "ep"}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue}, {Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue}}}}
			if tc.unobserved {
				dep.Status.ObservedGeneration = 1
			}
			if tc.deadline {
				dep.Status.Conditions = []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: tc.message}}
			}
			c := NewFakeK8sClient(t)
			if !tc.missing {
				require.NoError(t, c.Create(context.Background(), dep))
			}
			if tc.lingering {
				require.NoError(t, c.Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "test-namespace", Labels: map[string]string{"app": "inference", "endpoint": "ep"}}}))
			}
			status, err := o.getEndpointStats(c, "test-namespace", &v1.Cluster{}, ep)
			if tc.observeErr != nil {
				require.ErrorIs(t, err, tc.observeErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.phase, status.Phase)
			require.Contains(t, status.ErrorMessage, tc.message)
			if tc.phase == v1.EndpointPhaseDELETED {
				require.False(t, gate.observed)
				return
			}
			require.True(t, gate.observed)
			require.Equal(t, tc.missing, gate.dep == nil)
			require.Equal(t, tc.pending || tc.missing, gate.specPending)
		})
	}
}
