package neutreemetrics

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCGroupFSUsageReaderReadsCGroupV2Usage(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc", "1234"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sys/fs/cgroup/system.slice/docker-abc.scope"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "proc", "1234", "cgroup"),
		[]byte("0::/system.slice/docker-abc.scope\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "sys/fs/cgroup/system.slice/docker-abc.scope", "cpu.stat"),
		[]byte("usage_usec 12500000\nuser_usec 10000000\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "sys/fs/cgroup/system.slice/docker-abc.scope", "cpu.max"),
		[]byte("250000 100000\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "sys/fs/cgroup/system.slice/docker-abc.scope", "memory.current"),
		[]byte("1024\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "sys/fs/cgroup/system.slice/docker-abc.scope", "memory.stat"),
		[]byte("inactive_file 256\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "sys/fs/cgroup/system.slice/docker-abc.scope", "memory.max"),
		[]byte("4096\n"),
		0o644,
	))

	usage, ok, err := CGroupFSUsageReader{
		ProcFSRoot:   filepath.Join(root, "proc"),
		CGroupFSRoot: filepath.Join(root, "sys/fs/cgroup"),
	}.UsageForPID(1234)
	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, "abc", usage.ContainerID)
	assert.Equal(t, 12.5, usage.CPUUsageSeconds)
	assert.Equal(t, 1024.0, *usage.MemoryUsageBytes)
	assert.Equal(t, 768.0, *usage.MemoryWorkingSetBytes)
	assert.Equal(t, 2.5, *usage.CPULimitCores)
	assert.Equal(t, 4096.0, *usage.MemoryLimitBytes)
}

func TestRayServeRuntimeUsageProviderMapsReplicaToCGroupUsage(t *testing.T) {
	usageBytes := 1024.0
	cgroupReader := CGroupUsageReaderFunc(func(pid int) (ContainerRuntimeUsage, bool, error) {
		require.Equal(t, 1234, pid)

		return ContainerRuntimeUsage{
			ContainerID:      "docker-abc",
			CPUUsageSeconds:  12.5,
			MemoryUsageBytes: &usageBytes,
		}, true, nil
	})

	provider := RayServeRuntimeUsageProvider{
		Dashboard: fakeRuntimeDashboard{
			nodes: []v1.NodeSummary{
				{IP: "10.0.0.10", Raylet: v1.Raylet{NodeID: "node-a", State: v1.AliveNodeState}},
			},
			applications: &dashboard.RayServeApplicationsResponse{
				Applications: map[string]dashboard.RayServeApplicationStatus{
					"default_chat": {
						Deployments: map[string]dashboard.Deployment{
							"Backend": {
								Replicas: []dashboard.Replica{
									{NodeID: "node-a", ActorID: "actor-a", ReplicaID: "replica-a"},
								},
							},
						},
					},
				},
			},
			actors: map[string]dashboard.Actor{
				"actor-a": {ActorID: "actor-a", PID: 1234},
			},
		},
		Node:        "head-0",
		NodeIP:      "10.0.0.10",
		CGroupUsage: cgroupReader,
	}

	usages, err := provider.Usages(context.Background())
	require.NoError(t, err)
	require.Len(t, usages, 1)

	assert.Equal(t, "default", usages[0].Workspace)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "actor-a", usages[0].InstanceID)
	assert.Equal(t, "replica-a", usages[0].ReplicaID)
	assert.Equal(t, "head-0", usages[0].NodeID)
	assert.Equal(t, "Backend", usages[0].Deployment)
	assert.Equal(t, "docker-abc", usages[0].ContainerID)
	assert.Equal(t, 12.5, usages[0].CPUUsageSeconds)
	assert.Equal(t, 1024.0, *usages[0].MemoryUsageBytes)
}

func TestKubernetesCAdvisorRuntimeUsageProviderMapsPodMetricsToEndpointReplica(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "chat-abc",
			Labels: map[string]string{
				"app":            "inference",
				"cluster":        "k8s-a",
				"workspace":      "default",
				"endpoint":       "chat",
				"engine":         "vllm",
				"engine_version": "v0.17.1",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "node-a",
			Containers: []corev1.Container{
				{
					Name: "vllm",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2500m"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vllm", ContainerID: "containerd://container-abc"},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	provider := KubernetesCAdvisorRuntimeUsageProvider{
		Client:   client,
		NodeName: "node-a",
		Scraper: CAdvisorScraperFunc(func(_ context.Context) (string, error) {
			return `container_cpu_usage_seconds_total{namespace="default",pod="chat-abc",container="vllm",id="/kubepods/container-abc"} 12.5
container_memory_usage_bytes{namespace="default",pod="chat-abc",container="vllm"} 1024
container_memory_working_set_bytes{namespace="default",pod="chat-abc",container="vllm"} 768
`, nil
		}),
	}

	usages, err := provider.Usages(context.Background())
	require.NoError(t, err)
	require.Len(t, usages, 1)

	assert.Equal(t, "default", usages[0].Workspace)
	assert.Equal(t, "k8s-a", usages[0].Cluster)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "chat-abc", usages[0].InstanceID)
	assert.Equal(t, "chat-abc", usages[0].ReplicaID)
	assert.Equal(t, "node-a", usages[0].NodeID)
	assert.Equal(t, "vllm", usages[0].Container)
	assert.Equal(t, "container-abc", usages[0].ContainerID)
	assert.Equal(t, "vllm", usages[0].Engine)
	assert.Equal(t, "v0.17.1", usages[0].EngineVersion)
	assert.Equal(t, 12.5, usages[0].CPUUsageSeconds)
	assert.Equal(t, 1024.0, *usages[0].MemoryUsageBytes)
	assert.Equal(t, 768.0, *usages[0].MemoryWorkingSetBytes)
	assert.Equal(t, 2.5, *usages[0].CPULimitCores)
	assert.Equal(t, 4*1024*1024*1024.0, *usages[0].MemoryLimitBytes)
}

type fakeRuntimeDashboard struct {
	nodes        []v1.NodeSummary
	applications *dashboard.RayServeApplicationsResponse
	actors       map[string]dashboard.Actor
}

func (f fakeRuntimeDashboard) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return nil, nil
}

func (f fakeRuntimeDashboard) ListNodes() ([]v1.NodeSummary, error) {
	return f.nodes, nil
}

func (f fakeRuntimeDashboard) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, nil
}

func (f fakeRuntimeDashboard) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return f.applications, nil
}

func (f fakeRuntimeDashboard) UpdateServeApplications(_ dashboard.RayServeApplicationsRequest) error {
	return nil
}

func (f fakeRuntimeDashboard) GetActorLog(_, _ string, _ int) (string, error) {
	return "", nil
}

func (f fakeRuntimeDashboard) ListActors(filters []dashboard.ActorFilter, _ bool, _ int) (*dashboard.ActorsResponse, error) {
	actorID := ""
	for _, filter := range filters {
		if filter.Key == "actor_id" {
			actorID = filter.Value
		}
	}

	actor, ok := f.actors[actorID]
	if !ok {
		return &dashboard.ActorsResponse{}, nil
	}

	return &dashboard.ActorsResponse{
		Data: dashboard.ActorsResponseData{
			Result: dashboard.ActorsListResult{
				Result: []dashboard.Actor{actor},
			},
		},
	}, nil
}
