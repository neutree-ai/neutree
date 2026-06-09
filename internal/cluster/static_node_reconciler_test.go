package cluster

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestStaticNodeReconcilerReconcileComponentsStartsContainer(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name:          nodeExporterComponentName,
					Type:          v1.NodeComponentTypeNodeExporter,
					Image:         defaultNodeExporterImage,
					Args:          []string{"--path.rootfs=/host"},
					ConfigHash:    "hash-node-exporter",
					RestartPolicy: v1.NodeComponentRestartPolicyAlways,
					DockerRunOptions: []string{
						"--net=host",
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: defaultPrometheusHTTPPath,
						Port:     defaultNodeExporterPort,
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				contains: []string{"docker inspect", "'neutree-static-a-node-exporter'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'quay.io/prometheus/node-exporter:v1.8.2'",
			},
			{
				command: "docker rm -f 'neutree-static-a-node-exporter' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-node-exporter'",
					"--label 'neutree.ai/component-hash=hash-node-exporter'",
					"--restart unless-stopped",
					"--net=host",
					"'quay.io/prometheus/node-exporter:v1.8.2'",
					"'--path.rootfs=/host'",
				},
			},
			{
				command: "curl -fsS --max-time 5 'http://127.0.0.1:9100/metrics'",
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, "hash-node-exporter", statuses[0].ObservedHash)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsContinuesAfterIndependentFailure(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Type:       v1.NodeComponentTypeRayHead,
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray",
				},
				{
					Name:       nodeExporterComponentName,
					Type:       v1.NodeComponentTypeNodeExporter,
					Image:      defaultNodeExporterImage,
					ConfigHash: "hash-node-exporter",
					DockerRunOptions: []string{
						"--net=host",
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: defaultPrometheusHTTPPath,
						Port:     defaultNodeExporterPort,
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
				contains: []string{"docker inspect", "'neutree-static-a-node-exporter'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'quay.io/prometheus/node-exporter:v1.8.2'",
			},
			{
				command: "docker rm -f 'neutree-static-a-node-exporter' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-node-exporter'",
					"'quay.io/prometheus/node-exporter:v1.8.2'",
				},
			},
			{
				command: "curl -fsS --max-time 5 'http://127.0.0.1:9100/metrics'",
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.Error(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, v1.NodeComponentPhaseFailed, statuses[0].Phase)
	assert.Equal(t, componentReasonRunFailed, statuses[0].Reason)
	assert.True(t, statuses[1].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[1].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsUsesLocalImageWhenPullFails(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name:       nodeExporterComponentName,
					Type:       v1.NodeComponentTypeNodeExporter,
					Image:      defaultNodeExporterImage,
					ConfigHash: "hash-node-exporter",
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: defaultPrometheusHTTPPath,
						Port:     defaultNodeExporterPort,
					},
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				contains: []string{"docker inspect", "'neutree-static-a-node-exporter'"},
				err:      errors.New("not found"),
			},
			{
				command: "docker pull 'quay.io/prometheus/node-exporter:v1.8.2'",
				err:     errors.New("quay unavailable"),
			},
			{
				command: "docker image inspect 'quay.io/prometheus/node-exporter:v1.8.2' >/dev/null",
			},
			{
				command: "docker rm -f 'neutree-static-a-node-exporter' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-node-exporter'",
					"'quay.io/prometheus/node-exporter:v1.8.2'",
				},
			},
			{
				command: "curl -fsS --max-time 5 'http://127.0.0.1:9100/metrics'",
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestStaticNodeReconcilerReconcileComponentsRestartsWhenConfigChanged(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name:          vmagentComponentName,
					Type:          v1.NodeComponentTypeMetricsAgent,
					Image:         defaultVMAgentImage,
					ConfigHash:    "hash-vmagent",
					RestartPolicy: v1.NodeComponentRestartPolicyAlways,
					DockerRunOptions: []string{
						"--net=host",
					},
					ConfigFiles: []v1.NodeComponentConfigFile{
						{
							Path:         vmagentConfigPath,
							Content:      "scrape_configs: []\n",
							Mode:         "0644",
							Sudo:         true,
							Atomic:       true,
							CreateParent: true,
						},
					},
					Volumes: []v1.NodeComponentVolume{
						{
							HostPath:  vmagentConfigPath,
							MountPath: vmagentConfigPath,
							ReadOnly:  true,
						},
					},
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: defaultHealthHTTPPath,
						Port:     defaultVMAgentPort,
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
				command: "docker inspect --format='{{index .Config.Labels \"neutree.ai/component-hash\"}} {{.State.Running}}' 'neutree-static-a-vmagent'",
				output:  "hash-vmagent true\n",
			},
			{
				command: "docker pull 'victoriametrics/vmagent:v1.115.0'",
			},
			{
				command: "docker rm -f 'neutree-static-a-vmagent' >/dev/null 2>&1 || true",
			},
			{
				contains: []string{
					"docker run -d",
					"--name 'neutree-static-a-vmagent'",
					"-v '/etc/neutree/vmagent/config.yaml:/etc/neutree/vmagent/config.yaml:ro'",
					"'victoriametrics/vmagent:v1.115.0'",
				},
			},
			{
				command: "curl -fsS --max-time 5 'http://127.0.0.1:8429/health'",
			},
		},
	}

	statuses, err := (&StaticNodeReconciler{}).ReconcileComponents(context.Background(), node, runner)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, 1, fileClient.calls)
	assert.Equal(t, vmagentConfigPath, fileClient.path)
	assert.Equal(t, []byte("scrape_configs: []\n"), fileClient.content)
	assert.Equal(t, len(runner.responses), runner.calls)
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
	changed bool
	path    string
	content []byte
	calls   int
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
	_ string,
	_ commandrunner.RemoveFileOptions,
) error {
	return nil
}
