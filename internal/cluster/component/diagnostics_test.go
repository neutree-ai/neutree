package component

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTruncateDiagnosticMessagePreservesUTF8(t *testing.T) {
	input := strings.Repeat("a", maxDiagnosticBytes-1) + "界" + strings.Repeat("b", 10)

	output := truncateDiagnosticMessage(input)

	assert.True(t, utf8.ValidString(output))
	assert.True(t, strings.HasSuffix(output, "\n... truncated"))
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
