package cluster

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAuxRayComponentName       = "ray-worker"
	testConfigRayComponentName    = "ray-head"
	testDefaultRayImage           = "registry.example.com/neutree/neutree-serve:v1.2.0"
	testDefaultPrometheusHTTPPath = "/metrics"
	testDefaultHealthHTTPPath     = "/health"
	testRayConfigPath             = "/etc/neutree/ray/config.yaml"
	testRayFileSDPath             = "/etc/neutree/ray/file_sd/ray.json"
)

func TestStaticNodeReconcilerReconcileWarmImages(t *testing.T) {
	tests := []struct {
		name       string
		node       *v1.StaticNode
		runner     *fakeStaticNodeRunner
		wantReady  bool
		wantErr    bool
		wantImages []v1.WarmImageStatus
	}{
		{
			name: "no warm images is ready",
			node: &v1.StaticNode{Spec: &v1.StaticNodeSpec{}},
			runner: &fakeStaticNodeRunner{
				responses: nil,
			},
			wantReady: true,
		},
		{
			name: "existing required image skips pull",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						output:  "registry.example.com/neutree/serve@sha256:ready\n",
					},
				},
			},
			wantReady: true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:   "ray-runtime",
					Ref:    "registry.example.com/neutree/serve:v1.2.0",
					Ready:  true,
					Digest: "registry.example.com/neutree/serve@sha256:ready",
					Phase:  v1.WarmPhaseReady,
					Reason: warmReasonImageReady,
				},
			},
		},
		{
			name: "missing required image pulls then records digest",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						err:     errors.New("not found"),
					},
					{
						command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
					},
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						output:  "registry.example.com/neutree/serve@sha256:pulled\n",
					},
				},
			},
			wantReady: true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:   "ray-runtime",
					Ref:    "registry.example.com/neutree/serve:v1.2.0",
					Ready:  true,
					Digest: "registry.example.com/neutree/serve@sha256:pulled",
					Phase:  v1.WarmPhaseReady,
					Reason: warmReasonImagePulled,
				},
			},
		},
		{
			name: "optional image pull failure does not block required warm readiness",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "engine", Ref: "registry.example.com/neutree/engine:test", Required: false},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/engine:test'",
						err:     errors.New("not found"),
					},
					{
						command: "docker pull 'registry.example.com/neutree/engine:test'",
						err:     errors.New("pull denied"),
					},
				},
			},
			wantReady: true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:    "engine",
					Ref:     "registry.example.com/neutree/engine:test",
					Ready:   false,
					Phase:   v1.WarmPhaseFailed,
					Reason:  warmReasonImagePullFailed,
					Message: "failed to pull image registry.example.com/neutree/engine:test: pull denied",
				},
			},
		},
		{
			name: "required image pull failure returns error",
			node: staticNodeWithWarmImages([]v1.WarmImageSpec{
				{Name: "ray-runtime", Ref: "registry.example.com/neutree/serve:v1.2.0", Required: true},
			}),
			runner: &fakeStaticNodeRunner{
				responses: []fakeStaticNodeResponse{
					{
						command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
						err:     errors.New("not found"),
					},
					{
						command: "docker pull 'registry.example.com/neutree/serve:v1.2.0'",
						err:     errors.New("pull denied"),
					},
				},
			},
			wantReady: false,
			wantErr:   true,
			wantImages: []v1.WarmImageStatus{
				{
					Name:    "ray-runtime",
					Ref:     "registry.example.com/neutree/serve:v1.2.0",
					Ready:   false,
					Phase:   v1.WarmPhaseFailed,
					Reason:  warmReasonImagePullFailed,
					Message: "failed to pull image registry.example.com/neutree/serve:v1.2.0: pull denied",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := (&StaticNodeReconciler{}).ReconcileWarmImages(context.Background(), tt.node, tt.runner)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, status)
			assert.Equal(t, tt.wantReady, status.Ready)
			if len(tt.wantImages) > 0 {
				assert.Equal(t, tt.wantImages, status.Images)
			}
			assert.Equal(t, len(tt.runner.responses), tt.runner.calls)
		})
	}
}

func TestBuildStaticNodeStatusClearsPreviousErrorOnSuccess(t *testing.T) {
	node := &v1.StaticNode{
		Status: &v1.StaticNodeStatus{
			Phase:        v1.StaticNodePhaseFailed,
			ErrorMessage: "previous pull failure",
		},
	}
	result := &StaticNodeReconcileResult{
		Warm: &v1.WarmStatus{Ready: true},
		Components: []v1.NodeComponentStatus{
			{
				Name:  "ray-head",
				Ready: true,
				Phase: v1.NodeComponentPhaseRunning,
			},
		},
	}

	status := buildStaticNodeStatus(node, result, nil)

	assert.Equal(t, v1.StaticNodePhaseReady, status.Phase)
	assert.Empty(t, status.ErrorMessage)
}

func TestInspectDockerImageIgnoresSSHWarning(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker image inspect --format='{{index .RepoDigests 0}}' 'registry.example.com/neutree/serve:v1.2.0'",
				output:  "Warning: Permanently added '10.0.0.10' (ED25519) to the list of known hosts.\r\nregistry.example.com/neutree/serve@sha256:ready\n",
			},
		},
	}

	digest, err := inspectDockerImage(context.Background(), runner, "registry.example.com/neutree/serve:v1.2.0")

	require.NoError(t, err)
	assert.Equal(t, "registry.example.com/neutree/serve@sha256:ready", digest)
}

func TestComponentContainerMatchesIgnoresSSHWarning(t *testing.T) {
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-head'",
				output:  "Warning: Permanently added '10.0.0.10' (ED25519) to the list of known hosts.\r\nhash-ray true\n",
			},
		},
	}

	matches, err := componentContainerMatches(context.Background(), runner, "neutree-static-a-ray-head", "hash-ray")

	require.NoError(t, err)
	assert.True(t, matches)
}

func TestBuildDockerRunCommandQuotesDockerRunOptions(t *testing.T) {
	command := buildDockerRunCommand(
		&v1.StaticNode{Spec: &v1.StaticNodeSpec{Cluster: "static-a"}},
		v1.NodeComponentSpec{
			Name:  "ray-head",
			Image: testDefaultRayImage,
			DockerRunOptions: []string{
				"--net host",
				"--volume /data:/data; touch /tmp/pwned",
			},
		},
		"hash-ray",
	)

	assert.Contains(t, command, "'--net' 'host'")
	assert.Contains(t, command, "'--volume' '/data:/data;' 'touch' '/tmp/pwned'")
	assert.NotContains(t, command, "--volume /data:/data; touch /tmp/pwned")
	assert.NotContains(t, command, " -p ")
}

func TestStaticNodeReconcilerReconcileComponentsStartsContainer(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultPrometheusHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testAuxRayComponentName,
					Type:       v1.NodeComponentTypeRayWorker,
					Image:      testDefaultRayImage,
					Args:       []string{"--block"},
					ConfigHash: "hash-ray-worker",
					DockerRunOptions: []string{
						"--net=host",
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultPrometheusHTTPPath,
						Port:     healthPort,
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				contains: []string{"docker inspect", "'neutree-static-a-ray-worker'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
			},
			{
				command: "docker rm -f 'neutree-static-a-ray-worker' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-ray-worker'",
					"--label 'neutree.ai/component-hash=hash-ray-worker'",
					"--restart unless-stopped",
					"--net=host",
					"'registry.example.com/neutree/neutree-serve:v1.2.0'",
					"'--block'",
				},
			},
		},
	}

	statuses, err := staticNodeReconcilerWithHealthyRayNode(healthHost).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, "hash-ray-worker", statuses[0].ObservedHash)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsContinuesAfterIndependentFailure(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultPrometheusHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Type:       v1.NodeComponentTypeRayHead,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray",
				},
				{
					Name:       testAuxRayComponentName,
					Type:       v1.NodeComponentTypeRayWorker,
					Image:      testDefaultRayImage,
					ConfigHash: "hash-ray-worker",
					DockerRunOptions: []string{
						"--net=host",
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultPrometheusHTTPPath,
						Port:     healthPort,
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				contains: []string{"docker inspect", "'neutree-static-a-ray-head'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				err:     errors.New("pull denied"),
			},
			{
				command: "docker image inspect 'registry.example.com/neutree/neutree-serve:v1.2.0' >/dev/null",
				err:     errors.New("not found"),
			},
			{
				contains: []string{"docker inspect", "'neutree-static-a-ray-worker'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
			},
			{
				command: "docker rm -f 'neutree-static-a-ray-worker' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-ray-worker'",
					"'registry.example.com/neutree/neutree-serve:v1.2.0'",
				},
			},
		},
	}

	statuses, err := staticNodeReconcilerWithHealthyRayNode(healthHost).ReconcileComponents(context.Background(), node, runner)

	require.Error(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, v1.NodeComponentPhaseFailed, statuses[0].Phase)
	assert.Equal(t, componentReasonRunFailed, statuses[0].Reason)
	assert.True(t, statuses[1].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[1].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsUsesLocalImageWhenPullFails(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultPrometheusHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testAuxRayComponentName,
					Type:       v1.NodeComponentTypeRayWorker,
					Image:      testDefaultRayImage,
					ConfigHash: "hash-ray-worker",
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultPrometheusHTTPPath,
						Port:     healthPort,
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				contains: []string{"docker inspect", "'neutree-static-a-ray-worker'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				err:     errors.New("quay unavailable"),
			},
			{
				command: "docker image inspect 'registry.example.com/neutree/neutree-serve:v1.2.0' >/dev/null",
			},
			{
				command: "docker rm -f 'neutree-static-a-ray-worker' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-ray-worker'",
					"'registry.example.com/neutree/neutree-serve:v1.2.0'",
				},
			},
		},
	}

	statuses, err := staticNodeReconcilerWithHealthyRayNode(healthHost).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsRestartsWhenConfigChanged(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultHealthHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testConfigRayComponentName,
					Type:       v1.NodeComponentTypeRayHead,
					Image:      testDefaultRayImage,
					ConfigHash: "hash-ray-head",
					DockerRunOptions: []string{
						"--net=host",
					},
					ConfigFiles: []v1.NodeComponentConfigFile{
						{
							Path:         testRayConfigPath,
							Content:      "scrape_configs: []\n",
							Mode:         "0644",
							Sudo:         true,
							Atomic:       true,
							CreateParent: true,
						},
					},
					Volumes: []v1.NodeComponentVolume{
						{
							HostPath:  testRayConfigPath,
							MountPath: testRayConfigPath,
							ReadOnly:  true,
						},
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultHealthHTTPPath,
						Port:     healthPort,
					},
				},
			},
		},
	}
	fileClient := &fakeStaticNodeFileClient{changed: true}
	runner := &fakeStaticNodeRunner{
		fileClient: fileClient,
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-head'",
				output:  "hash-ray-head true\n",
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
			},
			{
				command: "docker rm -f 'neutree-static-a-ray-head' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-ray-head'",
					"-v '/etc/neutree/ray/config.yaml:/etc/neutree/ray/config.yaml:ro'",
					"'registry.example.com/neutree/neutree-serve:v1.2.0'",
				},
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, 1, fileClient.calls)
	assert.Equal(t, testRayConfigPath, fileClient.path)
	assert.Equal(t, []byte("scrape_configs: []\n"), fileClient.content)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsDoesNotRestartWhenOnlySkipRestartConfigChanged(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultHealthHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testConfigRayComponentName,
					Type:       v1.NodeComponentTypeRayHead,
					Image:      testDefaultRayImage,
					ConfigHash: "hash-ray-head",
					DockerRunOptions: []string{
						"--net=host",
					},
					ConfigFiles: []v1.NodeComponentConfigFile{
						{
							Path:                testRayFileSDPath,
							Content:             `[{"targets":["10.0.0.10:19100"]}]`,
							Mode:                "0644",
							Sudo:                true,
							Atomic:              true,
							CreateParent:        true,
							SkipRestartOnChange: true,
						},
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultHealthHTTPPath,
						Port:     healthPort,
					},
				},
			},
		},
	}
	fileClient := &fakeStaticNodeFileClient{changed: true}
	runner := &fakeStaticNodeRunner{
		fileClient: fileClient,
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-head'",
				output:  "hash-ray-head true\n",
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, 1, fileClient.calls)
	assert.Equal(t, testRayFileSDPath, fileClient.path)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsStopsRemovedComponent(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster:    "static-a",
			IP:         "10.0.0.11",
			Components: nil,
		},
		Status: &v1.StaticNodeStatus{
			Components: []v1.NodeComponentStatus{
				{
					Name:  "ray-worker",
					Type:  v1.NodeComponentTypeRayWorker,
					Phase: v1.NodeComponentPhaseRunning,
					Ready: true,
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker rm -f 'neutree-static-a-ray-worker' >/dev/null 2>&1 || true",
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseStopped, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerDeleteRemovesDesiredAndObservedComponents(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name: testConfigRayComponentName,
					Type: v1.NodeComponentTypeRayHead,
					ConfigFiles: []v1.NodeComponentConfigFile{
						{
							Path: testRayConfigPath,
							Sudo: true,
						},
					},
				},
			},
		},
		Status: &v1.StaticNodeStatus{
			Components: []v1.NodeComponentStatus{
				{
					Name:  testConfigRayComponentName,
					Type:  v1.NodeComponentTypeRayHead,
					Ready: true,
					Phase: v1.NodeComponentPhaseRunning,
				},
				{
					Name:  "ray-head",
					Type:  v1.NodeComponentTypeRayHead,
					Ready: true,
					Phase: v1.NodeComponentPhaseRunning,
				},
			},
		},
	}
	fileClient := &fakeStaticNodeFileClient{}
	runner := &fakeStaticNodeRunner{
		fileClient: fileClient,
		responses: []fakeStaticNodeResponse{
			{
				command: "docker rm -f 'neutree-static-a-ray-head' >/dev/null 2>&1 || true",
			},
			{
				command: "containers=$(docker ps -aq --filter label='neutree.ai/static-node-cluster=static-a'); " +
					"if [ -n \"$containers\" ]; then docker rm -f $containers >/dev/null 2>&1; fi",
			},
		},
	}

	err := (&StaticNodeReconciler{}).Delete(context.Background(), node, runner)

	require.NoError(t, err)
	assert.Equal(t, len(runner.responses), runner.calls)
	assert.Equal(t, []string{testRayConfigPath}, fileClient.removedPaths)
}

func TestStaticNodeReconcilerReconcileComponentsChecksRayWorkerWithDashboardAPI(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.11",
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-worker",
					Type:       v1.NodeComponentTypeRayWorker,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-worker",
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPHost: "10.0.0.10",
						Port:     defaultRayDashboardPort,
						RayNodeLabels: map[string]string{
							v1.NeutreeServingVersionLabel:    "v1.2.0",
							v1.NeutreeNodeProvisionTypeLabel: v1.StaticNodeProvisionType,
						},
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-worker'",
				output:  "hash-ray-worker true\n",
			},
		},
	}
	reconciler := &StaticNodeReconciler{
		NewDashboardService: func(dashboardURL string) dashboard.DashboardService {
			assert.Equal(t, "http://10.0.0.10:8265", dashboardURL)

			return &fakeStaticNodeDashboardService{
				nodes: []v1.NodeSummary{
					{
						IP: "10.0.0.11",
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel:    "v1.2.0",
								v1.NeutreeNodeProvisionTypeLabel: v1.StaticNodeProvisionType,
							},
						},
					},
				},
			}
		},
	}

	statuses, err := reconciler.ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsRejectsRayWorkerLabelMismatch(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.11",
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-worker",
					Type:       v1.NodeComponentTypeRayWorker,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-worker",
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPHost: "10.0.0.10",
						Port:     defaultRayDashboardPort,
						RayNodeLabels: map[string]string{
							v1.NeutreeServingVersionLabel:    "v1.2.0",
							v1.NeutreeNodeProvisionTypeLabel: v1.StaticNodeProvisionType,
						},
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-worker'",
				output:  "hash-ray-worker true\n",
			},
		},
	}
	reconciler := &StaticNodeReconciler{
		NewDashboardService: func(_ string) dashboard.DashboardService {
			return &fakeStaticNodeDashboardService{
				nodes: []v1.NodeSummary{
					{
						IP: "10.0.0.11",
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel:    "v1.0.1",
								v1.NeutreeNodeProvisionTypeLabel: v1.AutoScaleNodeProvisionType,
							},
						},
					},
				},
			}
		},
	}

	statuses, err := reconciler.ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseDegraded, statuses[0].Phase)
	assert.Equal(t, componentReasonHealthCheckFailed, statuses[0].Reason)
	assert.Contains(t, statuses[0].Message, "label neutree.ai/cluster-version")
}

func TestStaticNodeReconcilerReconcileComponentsWaitsForHeadBeforeRayWorker(t *testing.T) {
	node := &v1.StaticNode{
		Metadata: &v1.Metadata{Workspace: "default", Name: "worker-0"},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.11",
			Role:    v1.StaticNodeRoleWorker,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-worker",
					Type:       v1.NodeComponentTypeRayWorker,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-worker",
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{}
	reconciler := &StaticNodeReconciler{
		HeadReadyChecker: fakeStaticNodeHeadReadyChecker{ready: false},
	}

	statuses, err := reconciler.ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhasePending, statuses[0].Phase)
	assert.Equal(t, componentReasonHeadPending, statuses[0].Reason)
	assert.Equal(t, "head static node is not ready", statuses[0].Message)
	assert.Equal(t, 0, runner.calls)
}

func TestStaticNodeStoreHeadReadyCheckerReadsHeadStatusFromClusterNodes(t *testing.T) {
	reader := &fakeStaticNodeHeadReadyReader{
		nodes: []*v1.StaticNode{
			{
				Spec:   &v1.StaticNodeSpec{Cluster: "static-a", Role: v1.StaticNodeRoleHead},
				Status: &v1.StaticNodeStatus{Phase: v1.StaticNodePhaseReady},
			},
		},
	}
	checker := &StaticNodeStoreHeadReadyChecker{Reader: reader}
	node := &v1.StaticNode{
		Metadata: &v1.Metadata{Workspace: "default", Name: "worker-0"},
		Spec:     &v1.StaticNodeSpec{Cluster: "static-a", Role: v1.StaticNodeRoleWorker},
	}

	ready, err := checker.HeadReady(context.Background(), node)

	require.NoError(t, err)
	assert.True(t, ready)
	assert.Equal(t, "default", reader.workspace)
	assert.Equal(t, "static-a", reader.clusterName)
}

func TestStaticNodeReconcilerReconcileComponentsChecksRayHeadWithDashboardAPI(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.10",
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Type:       v1.NodeComponentTypeRayHead,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-head",
					HealthCheck: &v1.NodeComponentHealthCheck{
						Port: defaultRayDashboardPort,
						RayNodeLabels: map[string]string{
							v1.NeutreeServingVersionLabel: "v1.2.0",
						},
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-head'",
				output:  "hash-ray-head true\n",
			},
		},
	}
	reconciler := &StaticNodeReconciler{
		NewDashboardService: func(dashboardURL string) dashboard.DashboardService {
			assert.Equal(t, "http://10.0.0.10:8265", dashboardURL)

			return &fakeStaticNodeDashboardService{
				nodes: []v1.NodeSummary{
					{
						IP: "10.0.0.10",
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.2.0",
							},
						},
					},
				},
			}
		},
	}

	statuses, err := reconciler.ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsRejectsRayHeadLabelMismatch(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.10",
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Type:       v1.NodeComponentTypeRayHead,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-head",
					HealthCheck: &v1.NodeComponentHealthCheck{
						Port: defaultRayDashboardPort,
						RayNodeLabels: map[string]string{
							v1.NeutreeServingVersionLabel: "v1.2.0",
						},
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-ray-head'",
				output:  "hash-ray-head true\n",
			},
		},
	}
	reconciler := &StaticNodeReconciler{
		NewDashboardService: func(_ string) dashboard.DashboardService {
			return &fakeStaticNodeDashboardService{
				nodes: []v1.NodeSummary{
					{
						IP: "10.0.0.10",
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
							Labels: map[string]string{
								v1.NeutreeServingVersionLabel: "v1.0.1",
							},
						},
					},
				},
			}
		},
	}

	statuses, err := reconciler.ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseDegraded, statuses[0].Phase)
	assert.Equal(t, componentReasonHealthCheckFailed, statuses[0].Reason)
	assert.Contains(t, statuses[0].Message, "label neutree.ai/cluster-version")
}

func staticNodeWithWarmImages(images []v1.WarmImageSpec) *v1.StaticNode {
	return &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Warm: &v1.WarmSpec{
				Images: images,
			},
		},
	}
}

func newStaticNodeHealthServer(t *testing.T, path string, body string) (string, int) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)

			return
		}

		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	parsedURL, err := url.Parse(server.URL)
	require.NoError(t, err)

	host, portString, err := net.SplitHostPort(parsedURL.Host)
	require.NoError(t, err)

	port, err := strconv.Atoi(portString)
	require.NoError(t, err)

	return host, port
}

type fakeStaticNodeDashboardService struct {
	nodes []v1.NodeSummary
}

func staticNodeReconcilerWithHealthyRayNode(ip string) *StaticNodeReconciler {
	return &StaticNodeReconciler{
		NewDashboardService: func(string) dashboard.DashboardService {
			return &fakeStaticNodeDashboardService{
				nodes: []v1.NodeSummary{
					{
						IP: ip,
						Raylet: v1.Raylet{
							State: v1.AliveNodeState,
						},
					},
				},
			}
		},
	}
}

func (f *fakeStaticNodeDashboardService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return &dashboard.ClusterMetadataResponse{}, nil
}

func (f *fakeStaticNodeDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	return f.nodes, nil
}

func (f *fakeStaticNodeDashboardService) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, nil
}

func (f *fakeStaticNodeDashboardService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return nil, nil
}

func (f *fakeStaticNodeDashboardService) UpdateServeApplications(_ dashboard.RayServeApplicationsRequest) error {
	return nil
}

func (f *fakeStaticNodeDashboardService) GetActorLog(_ string, _ string, _ int) (string, error) {
	return "", nil
}

type fakeStaticNodeHeadReadyChecker struct {
	ready bool
	err   error
}

func (f fakeStaticNodeHeadReadyChecker) HeadReady(_ context.Context, _ *v1.StaticNode) (bool, error) {
	return f.ready, f.err
}

type fakeStaticNodeHeadReadyReader struct {
	nodes       []*v1.StaticNode
	workspace   string
	clusterName string
	err         error
}

func (f *fakeStaticNodeHeadReadyReader) ListStaticNodes(
	_ context.Context,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	f.workspace = workspace
	f.clusterName = clusterName

	return f.nodes, f.err
}

func (f *fakeStaticNodeDashboardService) ListActors(
	_ []dashboard.ActorFilter,
	_ bool,
	_ int,
) (*dashboard.ActorsResponse, error) {
	return nil, nil
}

type fakeStaticNodeRunner struct {
	responses  []fakeStaticNodeResponse
	fileClient *fakeStaticNodeFileClient
	calls      int
}

type fakeStaticNodeResponse struct {
	command  string
	contains []string
	output   string
	err      error
}

func (f *fakeStaticNodeRunner) Run(_ context.Context, command string) (string, error) {
	if f.calls >= len(f.responses) {
		return "", errors.New("unexpected command: " + command)
	}

	response := f.responses[f.calls]
	f.calls++

	if response.command != "" && response.command != command {
		return "", errors.New("unexpected command: " + command + ", want: " + response.command)
	}

	for _, value := range response.contains {
		if !strings.Contains(command, value) {
			return "", errors.New("unexpected command: " + command + ", missing: " + value)
		}
	}

	return response.output, response.err
}

func (f *fakeStaticNodeRunner) Files() commandrunner.FileClient {
	return f.fileClient
}

type fakeStaticNodeFileClient struct {
	changed      bool
	path         string
	content      []byte
	removedPaths []string
	calls        int
}

func (f *fakeStaticNodeFileClient) WriteFileIfChanged(
	_ context.Context,
	remotePath string,
	content []byte,
	_ commandrunner.WriteFileOptions,
) (bool, error) {
	f.calls++
	f.path = remotePath
	f.content = append([]byte{}, content...)

	return f.changed, nil
}

func (f *fakeStaticNodeFileClient) WriteFile(
	_ context.Context,
	remotePath string,
	content []byte,
	_ commandrunner.WriteFileOptions,
) error {
	f.calls++
	f.path = remotePath
	f.content = append([]byte{}, content...)

	return nil
}

func (f *fakeStaticNodeFileClient) ReadFile(
	_ context.Context,
	_ string,
	_ commandrunner.ReadFileOptions,
) ([]byte, error) {
	return nil, nil
}

func (f *fakeStaticNodeFileClient) Stat(
	_ context.Context,
	_ string,
	_ commandrunner.StatFileOptions,
) (*commandrunner.FileStat, error) {
	return nil, nil
}

func (f *fakeStaticNodeFileClient) Remove(
	_ context.Context,
	remotePath string,
	_ commandrunner.RemoveFileOptions,
) error {
	f.removedPaths = append(f.removedPaths, remotePath)

	return nil
}
