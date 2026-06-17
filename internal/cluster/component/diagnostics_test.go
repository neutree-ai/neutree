package component

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFormatStatusWithDiagnostics(t *testing.T) {
	assert.Equal(t, "base", FormatStatusWithDiagnostics("base", nil))
	assert.Equal(t, "base\nDiagnostics:\nline one\nline two", FormatStatusWithDiagnostics("base", []string{"line one", "line two"}))
}

func TestTruncateDiagnosticMessagePreservesUTF8(t *testing.T) {
	input := strings.Repeat("a", maxDiagnosticBytes-1) + "界" + strings.Repeat("b", 10)

	output := truncateDiagnosticMessage(input)

	assert.True(t, utf8.ValidString(output))
	assert.True(t, strings.HasSuffix(output, "\n... truncated"))
	assert.LessOrEqual(t, len(output), maxDiagnosticBytes)
}

func TestServiceDiagnosticsIncludesStatusAndEvents(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC))
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
					UID:       "current-service",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeLoadBalancer,
					ClusterIP: "10.96.0.10",
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Port:       8000,
							TargetPort: intstr.FromInt(8000),
							NodePort:   30080,
							Protocol:   corev1.ProtocolTCP,
						},
					},
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{{IP: "192.0.2.10"}},
					},
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service-event",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Service",
					Namespace: "default",
					Name:      "router-service",
					UID:       "current-service",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "SyncLoadBalancerFailed",
				Message:       "failed to ensure load balancer",
				LastTimestamp: now,
			},
		).
		Build()

	diagnostics := ServiceDiagnostics(context.Background(), fakeClient, "default", "router-service")
	message := strings.Join(diagnostics, "\n")

	assert.Contains(t, message, "service/router-service")
	assert.Contains(t, message, "type=LoadBalancer")
	assert.Contains(t, message, "clusterIP=10.96.0.10")
	assert.Contains(t, message, "http:8000/TCP->8000,nodePort=30080")
	assert.Contains(t, message, "ingress=192.0.2.10")
	assert.Contains(t, message, "event/router-service: Warning SyncLoadBalancerFailed")
}

func TestServiceDiagnosticsIgnoresEventsForRecreatedService(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC))
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
					UID:       "current-service",
				},
				Spec: corev1.ServiceSpec{
					Type:      corev1.ServiceTypeLoadBalancer,
					ClusterIP: "10.96.0.10",
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "old-router-service-event",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Service",
					Namespace: "default",
					Name:      "router-service",
					UID:       "old-service",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "OldFailure",
				Message:       "stale event from deleted service",
				LastTimestamp: now,
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "current-router-service-event",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Service",
					Namespace: "default",
					Name:      "router-service",
					UID:       "current-service",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "CurrentFailure",
				Message:       "current service event",
				LastTimestamp: now,
			},
		).
		Build()

	diagnostics := ServiceDiagnostics(context.Background(), fakeClient, "default", "router-service")
	message := strings.Join(diagnostics, "\n")

	assert.Contains(t, message, "CurrentFailure")
	assert.NotContains(t, message, "OldFailure")
	assert.NotContains(t, message, "stale event from deleted service")
}

func TestEventDiagnosticsUsesFieldSelector(t *testing.T) {
	recorder := &eventListSelectorRecorder{
		Client: fake.NewClientBuilder().Build(),
	}

	diagnostics := eventDiagnostics(context.Background(), recorder, "default", "Pod", "router-abc", "pod-uid")

	assert.Empty(t, diagnostics)
	if assert.NotEmpty(t, recorder.eventListCalls) {
		firstCall := recorder.eventListCalls[0]
		assert.Equal(t, "default", firstCall.namespace)
		assert.Equal(t, "Pod", firstCall.fields["involvedObject.kind"])
		assert.Equal(t, "router-abc", firstCall.fields["involvedObject.name"])
		assert.Equal(t, "pod-uid", firstCall.fields["involvedObject.uid"])
	}
}

func TestServiceDiagnosticsHandlesMissingService(t *testing.T) {
	fakeClient := fake.NewClientBuilder().Build()

	diagnostics := ServiceDiagnostics(context.Background(), fakeClient, "default", "missing-service")

	assert.Len(t, diagnostics, 1)
	assert.Contains(t, diagnostics[0], "service/missing-service: get failed")
}

func TestDeploymentDiagnosticsLimitsAbnormalPods(t *testing.T) {
	objects := []client.Object{
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: "default"},
			Spec:       appsv1.DeploymentSpec{Replicas: pointy.Int32(6)},
			Status:     appsv1.DeploymentStatus{Replicas: 6},
		},
	}
	for i := 0; i < 6; i++ {
		objects = append(objects, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("router-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"app": "router",
				},
			},
			Status: corev1.PodStatus{Phase: corev1.PodPending},
		})
	}
	fakeClient := fake.NewClientBuilder().WithObjects(objects...).Build()

	diagnostics := DeploymentDiagnostics(context.Background(), fakeClient, "default", "router", map[string]string{"app": "router"})

	podLines := 0
	for _, line := range diagnostics {
		if strings.HasPrefix(line, "pod/") {
			podLines++
		}
	}
	assert.Equal(t, maxDiagnosticObjects, podLines)
}

func TestPodDiagnosticsIncludesTerminatedContainersAndSkipsReadyPods(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithObjects(
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-terminated",
					Namespace: "default",
					UID:       "current-pod",
					Labels: map[string]string{
						"app": "router",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "router",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									Reason:   "Error",
									ExitCode: 1,
									Message:  "process exited",
								},
							},
						},
					},
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "old-router-terminated-event",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "default",
					Name:      "router-terminated",
					UID:       "old-pod",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "OldPodFailure",
				Message:       "stale pod event",
				LastTimestamp: metav1.NewTime(time.Date(2026, 6, 17, 10, 1, 0, 0, time.UTC)),
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "current-router-terminated-event",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "default",
					Name:      "router-terminated",
					UID:       "current-pod",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "CurrentPodFailure",
				Message:       "current pod event",
				LastTimestamp: metav1.NewTime(time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)),
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-ready",
					Namespace: "default",
					Labels: map[string]string{
						"app": "router",
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "router",
							Ready: true,
							State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
						},
					},
				},
			},
		).
		Build()

	diagnostics := podDiagnostics(context.Background(), fakeClient, "default", map[string]string{"app": "router"})
	message := strings.Join(diagnostics, "\n")

	assert.Contains(t, message, "pod/router-terminated")
	assert.Contains(t, message, "terminated reason=Error exitCode=1")
	assert.Contains(t, message, "CurrentPodFailure")
	assert.NotContains(t, message, "OldPodFailure")
	assert.NotContains(t, message, "stale pod event")
	assert.NotContains(t, message, "pod/router-ready")
}

type eventListSelectorRecorder struct {
	client.Client

	eventListCalls []eventListCall
}

type eventListCall struct {
	namespace string
	fields    map[string]string
}

func (r *eventListSelectorRecorder) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	listOptions := &client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(listOptions)
	}

	if _, ok := list.(*corev1.EventList); ok {
		r.eventListCalls = append(r.eventListCalls, eventListCall{
			namespace: listOptions.Namespace,
			fields:    exactFieldMatches(listOptions.FieldSelector),
		})
	}

	return r.Client.List(ctx, list, opts...)
}

func exactFieldMatches(selector fields.Selector) map[string]string {
	matches := map[string]string{}
	if selector == nil {
		return matches
	}

	for _, field := range []string{"involvedObject.kind", "involvedObject.name", "involvedObject.uid"} {
		value, ok := selector.RequiresExactMatch(field)
		if ok {
			matches[field] = value
		}
	}

	return matches
}
