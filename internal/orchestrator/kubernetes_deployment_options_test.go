package orchestrator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func TestApplyDeploymentOptionsUnset(t *testing.T) {
	objects, err := util.DecodeKubernetesManifest(deploymentOptionsManifest)
	require.NoError(t, err)
	before := objects.DeepCopy()

	require.NoError(t, applyDeploymentOptions(objects, deploymentOptionsEndpoint(nil), "ns-test", "flex"))
	assert.Equal(t, before, objects, "unset must preserve template defaults")
}

func TestApplyStartupTimeout(t *testing.T) {
	cases := []struct {
		name          string
		timeout       int64
		period        string
		wantDeadline  int32
		wantThreshold int32
	}{
		{name: "overrides hardcoded deadline and threshold", timeout: 300, wantDeadline: 300, wantThreshold: 30},
		{name: "rounds up to the next probe period", timeout: 301, wantDeadline: 301, wantThreshold: 31},
		{name: "uses the probe's effective period", timeout: 300, period: "7", wantDeadline: 300, wantThreshold: 43},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := deploymentOptionsManifest
			if tc.period != "" {
				manifest = strings.Replace(manifest, "periodSeconds: 10", "periodSeconds: "+tc.period, 1)
			}
			objects, err := util.DecodeKubernetesManifest(manifest)
			require.NoError(t, err)

			require.NoError(t, applyStartupTimeout(objects, "ns-test", "ep-test", "flex", tc.timeout))

			deployment := deploymentOptionsDeployment(t, objects, "ep-test")
			require.NotNil(t, deployment.Spec.ProgressDeadlineSeconds)
			assert.Equal(t, tc.wantDeadline, *deployment.Spec.ProgressDeadlineSeconds)

			probe := deploymentOptionsContainer(t, deployment, "flex").StartupProbe
			require.NotNil(t, probe)
			assert.Equal(t, tc.wantThreshold, probe.FailureThreshold)
			assert.Equal(t, "/health", probe.HTTPGet.Path)
			assert.Equal(t, int32(5), probe.InitialDelaySeconds)

			assert.Equal(t, int32(17), deploymentOptionsContainer(t, deployment, "sidecar").StartupProbe.FailureThreshold)
			assert.Len(t, deployment.Spec.Template.Spec.InitContainers, 1)
			assert.Equal(t, int32(2400), *deploymentOptionsDeployment(t, objects, "ep-test-helper").Spec.ProgressDeadlineSeconds)

			configured := objects.DeepCopy()
			require.NoError(t, applyStartupTimeout(objects, "ns-test", "ep-test", "flex", tc.timeout))
			assert.Equal(t, configured, objects, "repeated configuration must be idempotent")
		})
	}
}

func TestApplyStartupTimeoutWithoutEngineContainer(t *testing.T) {
	manifest := strings.Replace(deploymentOptionsManifest, "        - name: flex\n", "        - name: runtime\n", 1)
	objects, err := util.DecodeKubernetesManifest(manifest)
	require.NoError(t, err)

	require.NoError(t, applyStartupTimeout(objects, "ns-test", "ep-test", "flex", 300))

	deployment := deploymentOptionsDeployment(t, objects, "ep-test")
	assert.Equal(t, int32(300), *deployment.Spec.ProgressDeadlineSeconds)
	assert.Equal(t, int32(17), deploymentOptionsContainer(t, deployment, "sidecar").StartupProbe.FailureThreshold)
	assert.Equal(t, int32(120), deploymentOptionsContainer(t, deployment, "runtime").StartupProbe.FailureThreshold)
}

func TestApplyStartupTimeoutWithoutStartupProbe(t *testing.T) {
	manifest := strings.Replace(deploymentOptionsManifest, "          startupProbe:\n            httpGet:", "          readinessProbe:\n            httpGet:", 1)
	objects, err := util.DecodeKubernetesManifest(manifest)
	require.NoError(t, err)

	require.NoError(t, applyStartupTimeout(objects, "ns-test", "ep-test", "flex", 300))

	deployment := deploymentOptionsDeployment(t, objects, "ep-test")
	assert.Equal(t, int32(300), *deployment.Spec.ProgressDeadlineSeconds)

	container := deploymentOptionsContainer(t, deployment, "flex")
	assert.Nil(t, container.StartupProbe)
	require.NotNil(t, container.ReadinessProbe)
	assert.Equal(t, int32(120), container.ReadinessProbe.FailureThreshold)
}

func TestApplyStartupTimeoutRejectsUnsupportedTemplates(t *testing.T) {
	cases := []struct {
		name         string
		replacements []string
		wantErr      string
	}{
		{
			name: "missing endpoint Deployment", replacements: []string{"name: ep-test\n", "name: another\n"},
			wantErr: "template must define apps/v1 Deployment ns-test/ep-test",
		},
		{
			name: "deadline must exceed minReadySeconds", replacements: []string{"  progressDeadlineSeconds: 1200", "  minReadySeconds: 300\n  progressDeadlineSeconds: 1200"},
			wantErr: "timeout must be greater than Deployment minReadySeconds (300)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := strings.NewReplacer(tc.replacements...).Replace(deploymentOptionsManifest)
			objects, err := util.DecodeKubernetesManifest(manifest)
			require.NoError(t, err)
			before := objects.DeepCopy()

			err = applyStartupTimeout(objects, "ns-test", "ep-test", "flex", 300)
			require.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, before, objects, "invalid configuration must not modify rendered resources")
		})
	}
}

func deploymentOptionsEndpoint(options map[string]any) *v1.Endpoint {
	return &v1.Endpoint{
		Metadata: &v1.Metadata{Name: "ep-test", Workspace: "default"},
		Spec:     &v1.EndpointSpec{DeploymentOptions: options},
	}
}

func deploymentOptionsDeployment(t *testing.T, objects *unstructured.UnstructuredList, name string) *appsv1.Deployment {
	t.Helper()

	for i := range objects.Items {
		if objects.Items[i].GetKind() != deploymentKind || objects.Items[i].GetName() != name {
			continue
		}

		var deployment appsv1.Deployment
		require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(objects.Items[i].Object, &deployment))

		return &deployment
	}

	require.FailNowf(t, "Deployment not found", "%s", name)

	return nil
}

func deploymentOptionsContainer(t *testing.T, deployment *appsv1.Deployment, name string) corev1.Container {
	t.Helper()

	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == name {
			return container
		}
	}

	require.FailNowf(t, "container not found", "%s", name)

	return corev1.Container{}
}

const deploymentOptionsManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: ep-test
  namespace: ns-test
spec:
  progressDeadlineSeconds: 1200
  template:
    spec:
      initContainers:
        - name: init
          image: init:latest
      containers:
        - name: sidecar
          image: sidecar:latest
          startupProbe:
            tcpSocket:
              port: 9000
            failureThreshold: 17
        - name: flex
          image: flex:v0.1.2
          startupProbe:
            httpGet:
              path: /health
              port: 8000
            initialDelaySeconds: 5
            timeoutSeconds: 5
            periodSeconds: 10
            failureThreshold: 120
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ep-test-helper
  namespace: ns-test
spec:
  progressDeadlineSeconds: 2400
---
apiVersion: v1
kind: Service
metadata:
  name: ep-test
  namespace: ns-test
spec:
  ports:
    - port: 8000
`
