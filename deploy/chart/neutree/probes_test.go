package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func renderedContainer(t *testing.T, overrides map[string]interface{}, deployment, container string) corev1.Container {
	t.Helper()

	var d appsv1.Deployment
	renderedObject(t, renderChart(t, overrides), "Deployment", deployment, &d)

	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == container {
			return c
		}
	}

	t.Fatalf("container %s not found in %s", container, deployment)

	return corev1.Container{}
}

func TestDefaultReadinessProbes(t *testing.T) {
	for _, tc := range []struct {
		deployment, container, path string
		port                        intstr.IntOrString
	}{
		{"neutree-api", "neutree-api", "/health", intstr.FromString("http")},
		{"neutree-core", "neutree-core", "/healthz", intstr.FromString("server")},
		{"neutree-postgrest", "postgrest", "/ready", intstr.FromString("admin")},
	} {
		c := renderedContainer(t, nil, tc.deployment, tc.container)
		require.NotNil(t, c.ReadinessProbe, tc.deployment)
		require.NotNil(t, c.ReadinessProbe.HTTPGet, tc.deployment)
		require.Equal(t, tc.path, c.ReadinessProbe.HTTPGet.Path, tc.deployment)
		require.Equal(t, tc.port, c.ReadinessProbe.HTTPGet.Port, tc.deployment)
		requireNamedPort(t, c, tc.port.StrVal)
	}

	vector := renderedContainer(t, nil, "neutree-vector", "vector")
	require.NotNil(t, vector.ReadinessProbe.TCPSocket)
	require.Equal(t, intstr.FromString("aggregator-log"), vector.ReadinessProbe.TCPSocket.Port)
	requireNamedPort(t, vector, "aggregator-log")
}

func TestPostgrestAdminServer(t *testing.T) {
	c := renderedContainer(t, nil, "neutree-postgrest", "postgrest")

	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}

	require.Equal(t, "6433", env["PGRST_ADMIN_SERVER_PORT"])
	// Without a TCP user timeout a vanished database host leaves pooled
	// connections hanging for the kernel retransmission timeout.
	require.Contains(t, env["PGRST_DB_URI"], "/neutree?keepalives=1&")
	require.Contains(t, env["PGRST_DB_URI"], "tcp_user_timeout=10000")
	require.Equal(t, int32(6433), requireNamedPort(t, c, "admin"))

	require.NotNil(t, c.LivenessProbe)
	require.Equal(t, "/live", c.LivenessProbe.HTTPGet.Path)
	require.Equal(t, intstr.FromString("admin"), c.LivenessProbe.HTTPGet.Port)
}

func TestProbesOverridable(t *testing.T) {
	c := renderedContainer(t, map[string]interface{}{
		"api": map[string]interface{}{
			"readinessProbe": map[string]interface{}{"periodSeconds": 7},
		},
	}, "neutree-api", "neutree-api")
	require.Equal(t, int32(7), c.ReadinessProbe.PeriodSeconds)
	require.Equal(t, "/health", c.ReadinessProbe.HTTPGet.Path)

	c = renderedContainer(t, map[string]interface{}{
		"postgrest": map[string]interface{}{"livenessProbe": nil, "readinessProbe": nil},
	}, "neutree-postgrest", "postgrest")
	require.Nil(t, c.ReadinessProbe)
	require.Nil(t, c.LivenessProbe)
}

func requireNamedPort(t *testing.T, c corev1.Container, name string) int32 {
	t.Helper()

	for _, p := range c.Ports {
		if p.Name == name {
			return p.ContainerPort
		}
	}

	t.Fatalf("container %s has no port named %s", c.Name, name)

	return 0
}

func TestPreStopDelaysShutdown(t *testing.T) {
	for _, tc := range []struct{ deployment, container string }{
		{"neutree-api", "neutree-api"},
		{"neutree-auth", "auth"},
		{"neutree-vector", "vector"},
	} {
		c := renderedContainer(t, nil, tc.deployment, tc.container)
		require.NotNil(t, c.Lifecycle, tc.deployment)
		require.Equal(t, []string{"sleep", "5"}, c.Lifecycle.PreStop.Exec.Command, tc.deployment)
	}

	// The PostgREST image has no shell.
	c := renderedContainer(t, nil, "neutree-postgrest", "postgrest")
	require.Equal(t, int64(5), c.Lifecycle.PreStop.Sleep.Seconds)

	c = renderedContainer(t, map[string]interface{}{
		"api": map[string]interface{}{"lifecycle": nil},
	}, "neutree-api", "neutree-api")
	require.Nil(t, c.Lifecycle)
}

func TestKongShutdownOutlastsDrain(t *testing.T) {
	var d appsv1.Deployment
	renderedObject(t, renderChart(t, map[string]interface{}{
		"kong": map[string]interface{}{
			"shutdown": map[string]interface{}{"waitSeconds": 10, "drainTimeoutSeconds": 300},
		},
	}), "Deployment", "neutree-kong", &d)

	var proxy corev1.Container
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "proxy" {
			proxy = c
		}
	}

	cmd := proxy.Lifecycle.PreStop.Exec.Command
	require.Equal(t, []string{"/bin/bash", "-c"}, cmd[:2])
	require.Contains(t, cmd[2], "sleep 10\n")
	require.Contains(t, cmd[2], "deadline=$((SECONDS + 300))")
	require.Contains(t, cmd[2], "exec kong quit --timeout=10")
	// The grace period must cover the wait, the drain and the quit, or the
	// kubelet kills Kong while streams are still being served.
	require.Equal(t, int64(325), *d.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func TestPostgrestStaysReadyThroughSwitchover(t *testing.T) {
	p := renderedContainer(t, nil, "neutree-postgrest", "postgrest").ReadinessProbe

	// A planned primary switchover takes up to ~15s; every replica must stay
	// in the Service through it.
	require.Greater(t, p.PeriodSeconds*p.FailureThreshold, int32(15))
}
