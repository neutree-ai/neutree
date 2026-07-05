package staticnode

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
	commandrunner "github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
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

func TestReconcilerReconcileWarmImages(t *testing.T) {
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
			dockerRuntime := newTestDockerRuntime(t, tt.runner, nil)
			status, err := (&Reconciler{}).ReconcileWarmImages(context.Background(), tt.node, dockerRuntime)

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

func TestBuildStatusClearsPreviousErrorOnSuccess(t *testing.T) {
	node := &v1.StaticNode{
		Status: &v1.StaticNodeStatus{
			Phase:        v1.StaticNodePhaseFailed,
			ErrorMessage: "previous pull failure",
		},
	}
	result := &ReconcileResult{
		Warm: &v1.WarmStatus{Ready: true},
		Components: []v1.NodeComponentStatus{
			{
				Name:  "ray-head",
				Ready: true,
				Phase: v1.NodeComponentPhaseRunning,
			},
		},
	}

	status := BuildStatus(node, result, nil)

	assert.Equal(t, v1.StaticNodePhaseReady, status.Phase)
	assert.Empty(t, status.ErrorMessage)
}

func TestBuildStatusPreservesObservedScopesDuringPartialReconcile(t *testing.T) {
	const previousTransitionTime = "2026-07-03T01:02:03Z"

	node := &v1.StaticNode{
		Status: &v1.StaticNodeStatus{
			Phase:              v1.StaticNodePhaseReady,
			LastTransitionTime: previousTransitionTime,
			Accelerator:        &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU},
			Warm:               &v1.WarmStatus{Ready: true},
			Components: []v1.NodeComponentStatus{
				{
					Name:  "ray-head",
					Ready: true,
					Phase: v1.NodeComponentPhaseRunning,
				},
			},
		},
	}

	status := BuildStatus(node, &ReconcileResult{
		Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU},
	}, nil)

	assert.Equal(t, v1.StaticNodePhaseReady, status.Phase)
	assert.Equal(t, previousTransitionTime, status.LastTransitionTime)
	require.NotNil(t, status.Warm)
	assert.True(t, status.Warm.Ready)
	require.Len(t, status.Components, 1)
	assert.Equal(t, v1.NodeComponentPhaseRunning, status.Components[0].Phase)
}

func TestBuildStatusUpdatesTransitionTimeWhenPhaseChanges(t *testing.T) {
	const previousTransitionTime = "2026-07-03T01:02:03Z"

	node := &v1.StaticNode{
		Status: &v1.StaticNodeStatus{
			Phase:              v1.StaticNodePhaseReconciling,
			LastTransitionTime: previousTransitionTime,
		},
	}

	status := BuildStatus(node, &ReconcileResult{
		Warm: &v1.WarmStatus{Ready: true},
		Components: []v1.NodeComponentStatus{
			{
				Name:  "ray-head",
				Ready: true,
				Phase: v1.NodeComponentPhaseRunning,
			},
		},
	}, nil)

	assert.Equal(t, v1.StaticNodePhaseReady, status.Phase)
	assert.NotEmpty(t, status.LastTransitionTime)
	assert.NotEqual(t, previousTransitionTime, status.LastTransitionTime)
}

func TestBuildStatusWritesNodeDeviceSnapshotAllocations(t *testing.T) {
	status := BuildStatus(&v1.StaticNode{}, &ReconcileResult{
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{UUID: "GPU-abc", ProductName: "NVIDIA A100"},
			},
		},
		Allocations: []v1.StaticNodeAllocationStatus{
			{
				WorkloadType: "endpoint",
				Workspace:    "default",
				Endpoint:     "chat",
				ReplicaID:    "replica-a",
				Devices: []v1.DeviceAllocation{
					{UUID: "GPU-abc", Product: "NVIDIA_A100", MemoryMiB: 81920},
				},
			},
		},
	}, nil)

	require.NotNil(t, status.Accelerator)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), status.Accelerator.Type)
	require.Len(t, status.Allocations, 1)
	assert.Equal(t, "chat", status.Allocations[0].Endpoint)
	assert.Equal(t, "replica-a", status.Allocations[0].ReplicaID)
}

func TestBuildStatusEmptyComponentsReconciling(t *testing.T) {
	status := BuildStatus(&v1.StaticNode{}, &ReconcileResult{
		Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU},
		Warm:        &v1.WarmStatus{Ready: true},
		Components:  []v1.NodeComponentStatus{},
	}, nil)

	assert.Equal(t, v1.StaticNodePhaseReconciling, status.Phase)
	assert.Empty(t, status.ErrorMessage)
}

func TestReconcilerReconcileAcceleratorSkipsDetectionWhenCurrentStatusExists(t *testing.T) {
	node := staticNodeForDeviceSnapshot()
	node.Spec.SSHAuth = &v1.Auth{}
	node.Status = &v1.StaticNodeStatus{
		Accelerator: &v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{UUID: "GPU-abc", ProductModel: "NVIDIA_Tesla_T4"},
			},
		},
	}
	manager := &fakeAcceleratorManager{
		accelerator: &v1.StaticNodeAcceleratorStatus{Type: "amd_gpu"},
	}

	accelerator, err := (&Reconciler{
		AcceleratorManager: manager,
	}).ReconcileAccelerator(context.Background(), node, nil)

	require.NoError(t, err)
	require.NotNil(t, accelerator)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), accelerator.Type)
	require.Len(t, accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", accelerator.Devices[0].UUID)
	assert.Equal(t, 0, manager.calls)
}

func TestReconcilerReconcileAcceleratorDetectsWhenCurrentStatusIsEmpty(t *testing.T) {
	node := staticNodeForDeviceSnapshot()
	node.Spec.SSHAuth = &v1.Auth{}
	manager := &fakeAcceleratorManager{
		accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.AcceleratorTypeNVIDIAGPU.String()},
	}

	accelerator, err := (&Reconciler{
		AcceleratorManager: manager,
	}).ReconcileAccelerator(context.Background(), node, nil)

	require.NoError(t, err)
	require.NotNil(t, accelerator)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), accelerator.Type)
	assert.Equal(t, 1, manager.calls)
}

func TestReconcilerReconcileNodeDeviceSnapshotUsesAgentForDetails(t *testing.T) {
	client := &fakeNodeDeviceSnapshotClient{
		snapshot: &v1.NodeDeviceSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{
				Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				Devices: []v1.StaticNodeAcceleratorDeviceStatus{
					{
						UUID:         "GPU-abc",
						ProductName:  "NVIDIA A100",
						ProductModel: "NVIDIA_A100",
						MinorNumber:  v1.StaticNodeAcceleratorDeviceMinorNumberUnknown,
					},
				},
			},
			Allocations: []v1.StaticNodeAllocationStatus{
				{Endpoint: "chat", ReplicaID: "replica-a", Devices: []v1.DeviceAllocation{{UUID: "GPU-abc"}}},
			},
		},
	}

	accelerator, allocations, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		staticNodeForDeviceSnapshot(),
		&v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{UUID: "GPU-abc", ID: "3", MinorNumber: 3, MemoryMiB: 81920},
			},
		},
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, accelerator)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), accelerator.Type)
	require.Len(t, accelerator.Devices, 1)
	assert.Equal(t, "GPU-abc", accelerator.Devices[0].UUID)
	assert.Equal(t, "3", accelerator.Devices[0].ID)
	assert.Equal(t, 3, accelerator.Devices[0].MinorNumber)
	assert.Equal(t, int64(81920), accelerator.Devices[0].MemoryMiB)
	require.Len(t, allocations, 1)
	assert.Equal(t, "chat", allocations[0].Endpoint)
	assert.Equal(t, 1, client.calls)
}

func TestReconcilerReconcileNodeDeviceSnapshotKeepsMinorNumberZero(t *testing.T) {
	client := &fakeNodeDeviceSnapshotClient{
		snapshot: &v1.NodeDeviceSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{
				Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				Devices: []v1.StaticNodeAcceleratorDeviceStatus{
					{UUID: "GPU-abc", MinorNumber: 0},
				},
			},
		},
	}

	accelerator, _, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		staticNodeForDeviceSnapshot(),
		&v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{UUID: "GPU-abc", MinorNumber: 3},
			},
		},
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, accelerator)
	require.Len(t, accelerator.Devices, 1)
	assert.Equal(t, 0, accelerator.Devices[0].MinorNumber)
}

func TestReconcilerReconcileNodeDeviceSnapshotBackfillsUnknownMinorNumber(t *testing.T) {
	client := &fakeNodeDeviceSnapshotClient{
		snapshot: &v1.NodeDeviceSnapshot{
			Accelerator: v1.StaticNodeAcceleratorStatus{
				Type: v1.AcceleratorTypeNVIDIAGPU.String(),
				Devices: []v1.StaticNodeAcceleratorDeviceStatus{
					{UUID: "GPU-abc", MinorNumber: v1.StaticNodeAcceleratorDeviceMinorNumberUnknown},
				},
			},
		},
	}

	accelerator, _, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		staticNodeForDeviceSnapshot(),
		&v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
			Devices: []v1.StaticNodeAcceleratorDeviceStatus{
				{UUID: "GPU-abc", MinorNumber: 3},
			},
		},
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, accelerator)
	require.Len(t, accelerator.Devices, 1)
	assert.Equal(t, 3, accelerator.Devices[0].MinorNumber)
}

func TestReconcilerReconcileNodeDeviceSnapshotKeepsFallbackOnSnapshotError(t *testing.T) {
	node := staticNodeForDeviceSnapshot()
	node.Status = &v1.StaticNodeStatus{
		Allocations: []v1.StaticNodeAllocationStatus{
			{Endpoint: "previous", ReplicaID: "replica-old"},
		},
	}
	fallback := &v1.StaticNodeAcceleratorStatus{
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{UUID: "GPU-abc", ProductName: "NVIDIA A100"},
		},
	}
	client := &fakeNodeDeviceSnapshotClient{err: errors.New("snapshot timeout")}

	accelerator, allocations, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		node,
		fallback,
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	assert.Same(t, fallback, accelerator)
	require.Len(t, allocations, 1)
	assert.Equal(t, "previous", allocations[0].Endpoint)
	assert.Equal(t, 1, client.calls)
}

func TestReconcilerReconcileNodeDeviceSnapshotDoesNotDowngradeDetectedGPUToCPU(t *testing.T) {
	fallback := &v1.StaticNodeAcceleratorStatus{
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{UUID: "GPU-abc", ProductName: "NVIDIA A100"},
		},
	}
	client := &fakeNodeDeviceSnapshotClient{
		snapshot: &v1.NodeDeviceSnapshot{
			Accelerator: v1.CPUStaticNodeAcceleratorStatus(),
			Allocations: []v1.StaticNodeAllocationStatus{
				{Endpoint: "chat", ReplicaID: "replica-a"},
			},
		},
	}

	accelerator, allocations, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		staticNodeForDeviceSnapshot(),
		fallback,
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	assert.Same(t, fallback, accelerator)
	require.Len(t, allocations, 1)
	assert.Equal(t, "chat", allocations[0].Endpoint)
}

func TestReconcilerReconcileNodeDeviceSnapshotSkipsCPUFallback(t *testing.T) {
	node := staticNodeForDeviceSnapshot()
	node.Status = &v1.StaticNodeStatus{
		Allocations: []v1.StaticNodeAllocationStatus{
			{Endpoint: "previous", ReplicaID: "replica-old"},
		},
	}
	fallback := &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU}
	client := &fakeNodeDeviceSnapshotClient{
		snapshot: &v1.NodeDeviceSnapshot{
			Allocations: []v1.StaticNodeAllocationStatus{{Endpoint: "new", ReplicaID: "replica-new"}},
		},
	}

	accelerator, allocations, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		node,
		fallback,
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	assert.Same(t, fallback, accelerator)
	require.Len(t, allocations, 1)
	assert.Equal(t, "previous", allocations[0].Endpoint)
	assert.Equal(t, 0, client.calls)
}

func TestReconcilerReconcileNodeDeviceSnapshotWaitsForNodeAgentReady(t *testing.T) {
	node := staticNodeForDeviceSnapshot()
	node.Status = &v1.StaticNodeStatus{
		Allocations: []v1.StaticNodeAllocationStatus{
			{Endpoint: "previous", ReplicaID: "replica-old"},
		},
	}
	client := &fakeNodeDeviceSnapshotClient{
		snapshot: &v1.NodeDeviceSnapshot{
			Allocations: []v1.StaticNodeAllocationStatus{{Endpoint: "new"}},
		},
	}
	fallback := &v1.StaticNodeAcceleratorStatus{Type: v1.AcceleratorTypeNVIDIAGPU.String()}

	accelerator, allocations, err := (&Reconciler{NodeDeviceSnapshotClient: client}).ReconcileNodeDeviceSnapshot(
		context.Background(),
		node,
		fallback,
		[]v1.NodeComponentStatus{
			{Name: nodeAgentComponentName, Ready: false, Phase: v1.NodeComponentPhaseRunning},
		},
	)

	require.NoError(t, err)
	assert.Same(t, fallback, accelerator)
	require.Len(t, allocations, 1)
	assert.Equal(t, "previous", allocations[0].Endpoint)
	assert.Equal(t, 0, client.calls)
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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	digest, err := dockerRuntime.InspectImageDigest(
		context.Background(),
		"registry.example.com/neutree/serve:v1.2.0",
	)

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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	matches, err := dockerRuntime.ComponentContainerMatches(
		context.Background(),
		"neutree-static-a-ray-head",
		"hash-ray",
	)

	require.NoError(t, err)
	assert.True(t, matches)
}

func TestReconcilerReconcileComponentsFailsWhenImageMissing(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name: "ray-head",
				},
			},
		},
	}

	runner := &fakeStaticNodeRunner{}
	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.Error(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseFailed, statuses[0].Phase)
	assert.Equal(t, componentReasonImageMissing, statuses[0].Reason)
	assert.Contains(t, statuses[0].Message, "component image is required")
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

func TestReconcilerReconcileComponentsStartsContainer(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultPrometheusHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testAuxRayComponentName,
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
				command: "docker image inspect 'registry.example.com/neutree/neutree-serve:v1.2.0' >/dev/null",
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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, "hash-ray-worker", statuses[0].ObservedHash)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsContinuesAfterIndependentFailure(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultPrometheusHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray",
				},
				{
					Name:       testAuxRayComponentName,
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
				command: "docker image inspect 'registry.example.com/neutree/neutree-serve:v1.2.0' >/dev/null",
				err:     errors.New("not found"),
			},
			{
				command: "docker pull 'registry.example.com/neutree/neutree-serve:v1.2.0'",
				err:     errors.New("pull denied"),
			},
			{
				contains: []string{"docker inspect", "'neutree-static-a-ray-worker'"},
				err:      errors.New("not found"),
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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.Error(t, err)
	require.Len(t, statuses, 2)
	assert.Equal(t, v1.NodeComponentPhaseFailed, statuses[0].Phase)
	assert.Equal(t, componentReasonImagePullFailed, statuses[0].Reason)
	assert.True(t, statuses[1].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[1].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsUsesLocalImageWithoutPull(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultPrometheusHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testAuxRayComponentName,
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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsRestartsWhenConfigChanged(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultHealthHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testConfigRayComponentName,
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
				command: "docker image inspect 'registry.example.com/neutree/neutree-serve:v1.2.0' >/dev/null",
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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, 1, fileClient.calls)
	assert.Equal(t, testRayConfigPath, fileClient.path)
	assert.Equal(t, []byte("scrape_configs: []\n"), fileClient.content)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsDoesNotRestartWhenOnlySkipRestartConfigChanged(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultHealthHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       testConfigRayComponentName,
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

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, 1, fileClient.calls)
	assert.Equal(t, testRayFileSDPath, fileClient.path)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsStopsRemovedComponent(t *testing.T) {
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
					Phase: v1.NodeComponentPhaseRunning,
					Ready: true,
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{
		responses: []fakeStaticNodeResponse{
			{
				command: "mkdir -p '/etc/neutree/docker'",
			},
			{
				command: "docker rm -f 'neutree-static-a-ray-worker' >/dev/null 2>&1 || true",
			},
		},
	}

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseStopped, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerDeleteRemovesDesiredAndObservedComponents(t *testing.T) {
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Components: []v1.NodeComponentSpec{
				{
					Name: testConfigRayComponentName,
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
					Ready: true,
					Phase: v1.NodeComponentPhaseRunning,
				},
				{
					Name:  "ray-head",
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

	err := (&Reconciler{}).Delete(context.Background(), node, runner)

	require.NoError(t, err)
	assert.Equal(t, len(runner.responses), runner.calls)
	assert.Equal(t, []string{testRayConfigPath}, fileClient.removedPaths)
}

func TestReconcilerReconcileComponentsChecksRayWorkerHTTPProbe(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultHealthHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-worker",
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-worker",
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultHealthHTTPPath,
						Port:     healthPort,
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
	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsWaitsForHeadBeforeWorkerComponent(t *testing.T) {
	node := &v1.StaticNode{
		Metadata: &v1.Metadata{Workspace: "default", Name: "worker-0"},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.11",
			Role:    v1.StaticNodeRoleWorker,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "worker-runtime",
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-worker-runtime",
				},
			},
		},
	}
	runner := &fakeStaticNodeRunner{}
	reconciler := &Reconciler{
		HeadReadyChecker: fakeHeadReadyChecker{ready: false},
	}

	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := reconciler.ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhasePending, statuses[0].Phase)
	assert.Equal(t, componentReasonHeadPending, statuses[0].Reason)
	assert.Equal(t, "head static node is not ready", statuses[0].Message)
	assert.Equal(t, 1, runner.calls)
}

func TestClusterHeadReadyCheckerReadsHeadStatusFromClusterNodes(t *testing.T) {
	store := &fakeStaticNodeHeadReadyStore{
		nodes: []*v1.StaticNode{
			{
				Metadata: &v1.Metadata{Workspace: "default", Name: "head-0"},
				Spec:     &v1.StaticNodeSpec{Cluster: "static-a", Role: v1.StaticNodeRoleHead},
				Status:   &v1.StaticNodeStatus{Phase: v1.StaticNodePhaseReady},
			},
		},
	}
	checker := &ClusterHeadReadyChecker{Storage: store}
	node := &v1.StaticNode{
		Metadata: &v1.Metadata{Workspace: "default", Name: "worker-0"},
		Spec:     &v1.StaticNodeSpec{Cluster: "static-a", Role: v1.StaticNodeRoleWorker},
	}

	ready, err := checker.HeadReady(context.Background(), node)

	require.NoError(t, err)
	assert.True(t, ready)
	assert.Equal(t, "default", store.workspace)
	assert.Equal(t, "static-a", store.clusterName)
}

func TestReconcilerReconcileComponentsChecksRayHeadHTTPProbe(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, testDefaultHealthHTTPPath, `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-head",
					HealthCheck: &v1.NodeComponentHealthCheck{
						HTTPPath: testDefaultHealthHTTPPath,
						Port:     healthPort,
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
	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
	assert.Equal(t, len(runner.responses), runner.calls)
}

func TestReconcilerReconcileComponentsChecksHTTPRootWhenHTTPPathEmpty(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, "/", `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-head",
					HealthCheck: &v1.NodeComponentHealthCheck{
						Port: healthPort,
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
	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseRunning, statuses[0].Phase)
}

func TestReconcilerReconcileComponentsReportsHealthCheckFailureWithoutRestart(t *testing.T) {
	healthHost, healthPort := newStaticNodeHealthServer(t, "/health", `ok`)
	node := &v1.StaticNode{
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      healthHost,
			Components: []v1.NodeComponentSpec{
				{
					Name:       "ray-head",
					Image:      "registry.example.com/neutree/neutree-serve:v1.2.0",
					ConfigHash: "hash-ray-head",
					HealthCheck: &v1.NodeComponentHealthCheck{
						Port: healthPort,
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
	dockerRuntime := newTestDockerRuntime(t, runner, nil)
	statuses, err := (&Reconciler{}).ReconcileComponents(context.Background(), node, runner, dockerRuntime)

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.False(t, statuses[0].Ready)
	assert.Equal(t, v1.NodeComponentPhaseStarting, statuses[0].Phase)
	assert.Equal(t, componentReasonHealthCheckFailed, statuses[0].Reason)
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

type fakeHeadReadyChecker struct {
	ready bool
	err   error
}

func (f fakeHeadReadyChecker) HeadReady(_ context.Context, _ *v1.StaticNode) (bool, error) {
	return f.ready, f.err
}

type fakeNodeDeviceSnapshotClient struct {
	snapshot *v1.NodeDeviceSnapshot
	err      error
	calls    int
}

func (f *fakeNodeDeviceSnapshotClient) DeviceSnapshot(_ context.Context, _ *v1.StaticNode) (*v1.NodeDeviceSnapshot, error) {
	f.calls++

	return f.snapshot, f.err
}

type fakeAcceleratorManager struct {
	accelerator *v1.StaticNodeAcceleratorStatus
	err         error
	calls       int
}

func (f *fakeAcceleratorManager) DetectAccelerator(
	_ context.Context,
	_ string,
	_ v1.Auth,
) (*v1.StaticNodeAcceleratorStatus, error) {
	f.calls++

	return f.accelerator, f.err
}

func staticNodeForDeviceSnapshot() *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{Name: "head-0", Workspace: "default"},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			Role:    v1.StaticNodeRoleHead,
			IP:      "10.0.0.10",
		},
	}
}

type fakeStaticNodeHeadReadyStore struct {
	*storagemocks.MockStorage
	nodes       []*v1.StaticNode
	workspace   string
	clusterName string
	err         error
}

func (f *fakeStaticNodeHeadReadyStore) ListStaticNode(option storage.ListOption) ([]v1.StaticNode, error) {
	if f.err != nil {
		return nil, f.err
	}

	for _, filter := range option.Filters {
		switch filter.Column {
		case "metadata->>workspace":
			f.workspace = filter.Value
		case "spec->>cluster":
			f.clusterName = filter.Value
		}
	}

	items := make([]v1.StaticNode, 0, len(f.nodes))
	for _, node := range f.nodes {
		if node != nil {
			items = append(items, *node)
		}
	}

	return items, nil
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

func (f *fakeStaticNodeRunner) Close() error {
	return nil
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

func (f *fakeStaticNodeFileClient) Remove(
	_ context.Context,
	remotePath string,
	_ commandrunner.RemoveFileOptions,
) error {
	f.removedPaths = append(f.removedPaths, remotePath)

	return nil
}
