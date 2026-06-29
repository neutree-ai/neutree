package cluster

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterReconcilerBuildDesiredNodes(t *testing.T) {
	cluster := testStaticNodeCluster()
	profiles := map[string]*v1.AcceleratorProfile{
		v1.AcceleratorTypeNVIDIAGPU.String(): {
			AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
			ClusterRuntime: &v1.RuntimeConfig{
				Runtime: "nvidia",
				Env: map[string]string{
					"ACCELERATOR_TYPE": "gpu",
				},
				Options: []string{"--gpus all"},
			},
			Metrics: &v1.AcceleratorMetricsProfile{
				Exporter: &v1.AcceleratorExporterProfile{
					Kind:          "dcgm-exporter",
					ComponentType: v1.NodeComponentTypeAcceleratorExporter,
					Image:         "nvcr.io/nvidia/k8s/dcgm-exporter:test",
					Args:          []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"},
					Port:          9400,
					MetricsPath:   "/dcgm/metrics",
					ConfigFiles: []v1.NodeComponentConfigFile{
						{
							Path:    "/etc/neutree/dcgm-exporter/default-counters.csv",
							Content: "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature.",
						},
					},
					Runtime: &v1.AcceleratorExporterRuntimeProfile{
						HostNetwork: true,
						Capabilities: &v1.AcceleratorExporterCapabilities{
							Add: []string{"SYS_ADMIN"},
						},
						DockerRunOptions: []string{"--gpus all"},
					},
				},
			},
		},
	}

	currentNodes := []*v1.StaticNode{
		staticNodeStatusWithAccelerator(
			"head-0",
			v1.StaticNodeRoleHead,
			v1.StaticNodePhaseReady,
			true,
			nvidiaAcceleratorStatus(),
			nil,
		),
		staticNodeStatusWithAccelerator(
			"worker-0",
			v1.StaticNodeRoleWorker,
			v1.StaticNodePhaseReady,
			true,
			cpuAcceleratorStatus(),
			nil,
		),
	}
	reconciler := &StaticNodeClusterReconciler{
		RuntimeProfileProvider: fakeRuntimeProfileProvider{profiles: profiles},
	}

	nodes, err := reconciler.BuildDesiredNodes(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	require.Len(t, nodes, 2)

	head := nodes[0]
	require.NotNil(t, head.Metadata)
	require.NotNil(t, head.Spec)
	assert.Equal(t, "head-0", head.Metadata.Name)
	assert.Equal(t, "default", head.Metadata.Workspace)
	assert.Equal(t, "static-a", head.Spec.Cluster)
	assert.Equal(t, v1.StaticNodeRoleHead, head.Spec.Role)
	assert.Equal(t, "10.0.0.10", head.Spec.IP)
	assert.Equal(t, "ssh-ref", head.Spec.SSHAuthRef)
	require.NotNil(t, head.Spec.SSHAuth)
	assert.Equal(t, "ray", head.Spec.SSHAuth.SSHUser)
	assert.Equal(t, map[string]string{
		staticNodeClusterLabelKey: "static-a",
		staticNodeRoleLabelKey:    string(v1.StaticNodeRoleHead),
	}, head.Metadata.Labels)
	rayHead := findComponent(head.Spec.Components, "ray-head")
	require.NotNil(t, rayHead)
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-serve:v1.2.0", rayHead.Image)
	assert.Equal(t, []string{"/bin/bash", "-lc"}, rayHead.Command)
	require.Len(t, rayHead.Args, 1)
	assert.Contains(t, rayHead.Args[0], "python /home/ray/start.py --head")
	assert.Contains(t, rayHead.Args[0], "docker rm -f ray_container")
	assert.Contains(t, rayHead.Args[0], "(while true; do docker rm -f ray_container")
	assert.NotContains(t, rayHead.Args[0], "& &&")
	assert.Less(t,
		strings.Index(rayHead.Args[0], "(while true; do docker rm -f ray_container"),
		strings.Index(rayHead.Args[0], "python /home/ray/start.py --head"),
	)
	assert.Contains(t, rayHead.Args[0], "--dashboard-port=8265")
	assert.Contains(t, rayHead.Args[0], v1.NeutreeServingVersionLabel)
	assert.NotContains(t, rayHead.Args[0], "--autoscaling-config")
	require.NotNil(t, rayHead.HealthCheck)
	assert.Equal(t, map[string]string{
		v1.NeutreeServingVersionLabel: "v1.2.0",
	}, rayHead.HealthCheck.RayNodeLabels)
	assert.Equal(t, "gpu", rayHead.Env["ACCELERATOR_TYPE"])
	assert.Contains(t, rayHead.DockerRunOptions, "--runtime=nvidia")
	assert.Contains(t, rayHead.DockerRunOptions, "--gpus all")
	require.NotNil(t, head.Spec.Warm)
	assertWarmImages(t, head.Spec.Warm.Images, map[string]string{
		"ray-runtime":                    "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
		nodeExporterComponentName:        "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
		acceleratorExporterComponentName: "registry.example.com/neutree/nvidia/k8s/dcgm-exporter:test",
		nodeAgentComponentName:           "registry.example.com/neutree/neutree/neutree-node-agent:latest",
		vmagentComponentName:             "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0",
	})
	assertNodeComponentTypes(t, head.Spec.Components, []v1.NodeComponentType{
		v1.NodeComponentTypeRayHead,
		v1.NodeComponentTypeNodeExporter,
		v1.NodeComponentTypeAcceleratorExporter,
		v1.NodeComponentTypeNodeAgent,
		v1.NodeComponentTypeMetricsAgent,
	})
	nodeExporter := findComponent(head.Spec.Components, nodeExporterComponentName)
	require.NotNil(t, nodeExporter)
	assert.Equal(t, "registry.example.com/neutree/prometheus/node-exporter:v1.8.2", nodeExporter.Image)
	assert.Contains(t, nodeExporter.Args, "--web.listen-address=:19100")
	assert.Equal(t, 19100, nodeExporter.Ports[0].Port)
	require.NotNil(t, nodeExporter.HealthCheck)
	assert.Equal(t, 19100, nodeExporter.HealthCheck.Port)
	exporter := findComponent(head.Spec.Components, acceleratorExporterComponentName)
	require.NotNil(t, exporter)
	assert.Equal(t, "registry.example.com/neutree/nvidia/k8s/dcgm-exporter:test", exporter.Image)
	assert.Equal(t, []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"}, exporter.Args)
	assert.Equal(t, []string{"--net=host", "--cap-add=SYS_ADMIN", "--gpus all"}, exporter.DockerRunOptions)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature.", exporter.ConfigFiles[0].Content)
	assert.Equal(t, "/etc/neutree/dcgm-exporter/default-counters.csv", exporter.Volumes[0].MountPath)
	assert.Equal(t, 9400, exporter.Ports[0].Port)
	require.NotNil(t, exporter.HealthCheck)
	assert.Equal(t, "/dcgm/metrics", exporter.HealthCheck.HTTPPath)

	nodeAgent := findComponent(head.Spec.Components, nodeAgentComponentName)
	require.NotNil(t, nodeAgent)
	assert.Equal(t, v1.NodeComponentTypeNodeAgent, nodeAgent.Type)
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-node-agent:latest", nodeAgent.Image)
	assert.Contains(t, nodeAgent.Args, "--cluster-type=ray")
	assert.Contains(t, nodeAgent.Args, "--workspace=default")
	assert.Contains(t, nodeAgent.Args, "--cluster=static-a")
	assert.Contains(t, nodeAgent.Args, "--static-node-cluster=static-a")
	assert.Contains(t, nodeAgent.Args, "--node=head-0")
	assert.Contains(t, nodeAgent.Args, "--node-ip=10.0.0.10")
	assert.Contains(t, nodeAgent.Args, "--node-role=head")
	assert.Contains(t, nodeAgent.Args, "--listen-address=:19101")
	assert.NotContains(t, nodeAgent.Args, "--listen-address=:9101")
	assert.Contains(t, nodeAgent.Args, "--node-exporter-url=http://127.0.0.1:19100/metrics")
	assert.Contains(t, nodeAgent.Args, "--accelerator-exporter-url=http://127.0.0.1:9400/dcgm/metrics")
	assert.Contains(t, nodeAgent.Args, "--ray-dashboard-url=http://10.0.0.10:8265")
	assert.Contains(t, nodeAgent.Args, "--procfs-root=/host/proc")
	assert.Contains(t, nodeAgent.Args, "--cgroupfs-root=/host/sys/fs/cgroup")
	assert.Equal(t, []string{"--net=host", "--pid=host", "--cgroupns=host", "--gpus all"}, nodeAgent.DockerRunOptions)
	assert.Contains(t, nodeAgent.Volumes, v1.NodeComponentVolume{
		Name:      "host-proc",
		HostPath:  "/proc",
		MountPath: "/host/proc",
		ReadOnly:  true,
	})
	assert.Contains(t, nodeAgent.Volumes, v1.NodeComponentVolume{
		Name:      "host-cgroup",
		HostPath:  "/sys/fs/cgroup",
		MountPath: "/host/sys/fs/cgroup",
		ReadOnly:  true,
	})
	assert.Equal(t, 19101, nodeAgent.Ports[0].Port)
	require.NotNil(t, nodeAgent.HealthCheck)
	assert.Equal(t, defaultHealthHTTPPath, nodeAgent.HealthCheck.HTTPPath)
	assert.Equal(t, 19101, nodeAgent.HealthCheck.Port)

	vmagentComponent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagentComponent)
	assert.Equal(t, "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0", vmagentComponent.Image)
	assert.Contains(t, vmagentComponent.Args, "-remoteWrite.url=http://vm:8480/insert/0/prometheus/")
	assert.NotEmpty(t, vmagentComponent.ConfigHash)
	vmagentConfig := findConfigFile(vmagentComponent.ConfigFiles, vmagentConfigPath)
	require.NotNil(t, vmagentConfig)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-node-exporter`)
	assert.Contains(t, vmagentConfig.Content, `file_sd_configs:`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/node-exporter.json`)
	assert.NotContains(t, vmagentConfig.Content, `"10.0.0.10:19100"`)
	assert.NotContains(t, vmagentConfig.Content, `"10.0.0.11:19100"`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-node-agent`)
	assert.Contains(t, vmagentConfig.Content, "job_name: static-node-node-agent\n  honor_labels: true\n")
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/node-agent.json`)
	assert.NotContains(t, vmagentConfig.Content, `"10.0.0.10:9101"`)
	assert.NotContains(t, vmagentConfig.Content, `"10.0.0.11:9101"`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-ray`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/ray.json`)
	assert.Contains(t, vmagentConfig.Content, `metric_relabel_configs:`)
	assert.Contains(t, vmagentConfig.Content, `regex: 'ray_vllm[:_](.+)'`)
	assert.Contains(t, vmagentConfig.Content, `replacement: 'vllm:$1'`)
	assert.Contains(t, vmagentConfig.Content, `regex: 'ray_sglang[:_](.+)'`)
	assert.Contains(t, vmagentConfig.Content, `replacement: 'sglang_$1'`)
	assert.Contains(t, vmagentConfig.Content, `action: labeldrop`)
	assert.Contains(t, vmagentConfig.Content, `cache_dtype`)
	assert.Contains(t, vmagentConfig.Content, `num_gpu_blocks_override`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-accelerator-exporter-dcgm-metrics`)
	assert.Contains(t, vmagentConfig.Content, `metrics_path: "/dcgm/metrics"`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/accelerator-exporter-dcgm-metrics.json`)
	assert.NotContains(t, vmagentConfig.Content, `exporter_kind`)
	assert.NotContains(t, vmagentConfig.Content, `remote_write:`)
	assert.NotContains(t, vmagentConfig.Content, `"http://vm:8480/insert/0/prometheus/"`)
	nodeTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/node-exporter.json")
	require.NotNil(t, nodeTargets)
	assert.True(t, nodeTargets.SkipRestartOnChange)
	assert.Contains(t, nodeTargets.Content, `"targets": [`)
	assert.Contains(t, nodeTargets.Content, `"10.0.0.10:19100"`)
	assert.Contains(t, nodeTargets.Content, `"10.0.0.11:19100"`)
	assert.Contains(t, nodeTargets.Content, `"neutree_cluster": "static-a"`)
	assert.Contains(t, nodeTargets.Content, `"static_node_cluster": "static-a"`)
	nodeAgentTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/node-agent.json")
	require.NotNil(t, nodeAgentTargets)
	assert.True(t, nodeAgentTargets.SkipRestartOnChange)
	assert.Contains(t, nodeAgentTargets.Content, `"10.0.0.10:19101"`)
	assert.Contains(t, nodeAgentTargets.Content, `"10.0.0.11:19101"`)
	assert.NotContains(t, nodeAgentTargets.Content, `"10.0.0.10:9101"`)
	assert.NotContains(t, nodeAgentTargets.Content, `"10.0.0.11:9101"`)
	assert.Contains(t, nodeAgentTargets.Content, `"source": "neutree-node-agent"`)
	rayTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/ray.json")
	require.NotNil(t, rayTargets)
	assert.True(t, rayTargets.SkipRestartOnChange)
	assert.Contains(t, rayTargets.Content, `"10.0.0.10:54311"`)
	assert.Contains(t, rayTargets.Content, `"10.0.0.11:54311"`)
	assert.Contains(t, rayTargets.Content, `"source": "ray"`)
	acceleratorTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/accelerator-exporter-dcgm-metrics.json")
	require.NotNil(t, acceleratorTargets)
	assert.True(t, acceleratorTargets.SkipRestartOnChange)
	assert.Contains(t, acceleratorTargets.Content, `"10.0.0.10:9400"`)
	assert.NotContains(t, acceleratorTargets.Content, `"10.0.0.11:9400"`)
	assert.NotContains(t, acceleratorTargets.Content, `"accelerator_type"`)
	assert.NotContains(t, acceleratorTargets.Content, `"accelerator_exporter"`)
	assert.NotContains(t, acceleratorTargets.Content, `"accelerator_product_model"`)
	assert.NotContains(t, acceleratorTargets.Content, `"accelerator_vendor"`)

	worker := nodes[1]
	require.NotNil(t, worker.Metadata)
	require.NotNil(t, worker.Spec)
	assert.Equal(t, "worker-0", worker.Metadata.Name)
	assert.Equal(t, v1.StaticNodeRoleWorker, worker.Spec.Role)
	rayWorker := findComponent(worker.Spec.Components, "ray-worker")
	require.NotNil(t, rayWorker)
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-serve:v1.2.0", rayWorker.Image)
	require.Len(t, rayWorker.Args, 1)
	assert.Contains(t, rayWorker.Args[0], "python /home/ray/start.py --address=10.0.0.10:6379")
	assert.Contains(t, rayWorker.Args[0], "docker rm -f ray_container")
	assert.Contains(t, rayWorker.Args[0], "(while true; do docker rm -f ray_container")
	assert.NotContains(t, rayWorker.Args[0], "& &&")
	assert.Less(t,
		strings.Index(rayWorker.Args[0], "(while true; do docker rm -f ray_container"),
		strings.Index(rayWorker.Args[0], "python /home/ray/start.py --address=10.0.0.10:6379"),
	)
	assert.Contains(t, rayWorker.Args[0], v1.StaticNodeProvisionType)
	require.NotNil(t, rayWorker.HealthCheck)
	assert.Equal(t, "10.0.0.10", rayWorker.HealthCheck.HTTPHost)
	assert.Equal(t, defaultRayDashboardPort, rayWorker.HealthCheck.Port)
	assert.Equal(t, map[string]string{
		v1.NeutreeServingVersionLabel:    "v1.2.0",
		v1.NeutreeNodeProvisionTypeLabel: v1.StaticNodeProvisionType,
	}, rayWorker.HealthCheck.RayNodeLabels)
	assertNodeComponentTypes(t, worker.Spec.Components, []v1.NodeComponentType{
		v1.NodeComponentTypeRayWorker,
		v1.NodeComponentTypeNodeExporter,
		v1.NodeComponentTypeNodeAgent,
	})
	assertWarmImages(t, worker.Spec.Warm.Images, map[string]string{
		"ray-runtime":             "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
		nodeExporterComponentName: "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
		nodeAgentComponentName:    "registry.example.com/neutree/neutree/neutree-node-agent:latest",
	})

	cluster.Spec.Version = "mutated"
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-serve:v1.2.0", warmImageRef(head.Spec.Warm.Images, "ray-runtime"))
}

func TestAcceleratorExporterComponentTypeDefaults(t *testing.T) {
	assert.Equal(t,
		v1.NodeComponentTypeAcceleratorExporter,
		acceleratorExporterComponentType(&v1.AcceleratorExporterProfile{}),
	)
}

func TestStaticComponentImageUsesStaticRegistry(t *testing.T) {
	cluster := testStaticNodeCluster()

	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "strips source registry",
			image: "nvcr.io/nvidia/k8s/dcgm-exporter:test",
			want:  "registry.example.com/neutree/nvidia/k8s/dcgm-exporter:test",
		},
		{
			name:  "keeps docker hub repository path",
			image: "victoriametrics/vmagent:v1.115.0",
			want:  "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0",
		},
		{
			name:  "strips localhost registry",
			image: "localhost:5000/custom/exporter:v1",
			want:  "registry.example.com/neutree/custom/exporter:v1",
		},
		{
			name:  "keeps digest",
			image: "quay.io/prometheus/node-exporter@sha256:abc",
			want:  "registry.example.com/neutree/prometheus/node-exporter@sha256:abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, staticComponentImage(cluster, tt.image))
		})
	}
}

func TestStaticNodeClusterReconcilerBuildDesiredNodesValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*v1.StaticNodeCluster)
		wantErr string
	}{
		{
			name: "missing head node",
			mutate: func(cluster *v1.StaticNodeCluster) {
				for i := range cluster.Spec.Nodes {
					cluster.Spec.Nodes[i].Role = v1.StaticNodeRoleWorker
				}
			},
			wantErr: "static node cluster requires exactly one head node, got 0",
		},
		{
			name: "multiple head nodes",
			mutate: func(cluster *v1.StaticNodeCluster) {
				for i := range cluster.Spec.Nodes {
					cluster.Spec.Nodes[i].Role = v1.StaticNodeRoleHead
				}
			},
			wantErr: "static node cluster requires exactly one head node, got 2",
		},
		{
			name: "missing version",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Version = ""
			},
			wantErr: "static node cluster spec.version is required",
		},
		{
			name: "missing image registry",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.ImageRegistry = ""
			},
			wantErr: "static node cluster spec.image_registry is required",
		},
		{
			name: "duplicate node",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Nodes[0].Name = "head-0"
			},
			wantErr: "duplicate static node head-0",
		},
		{
			name: "missing ip",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Nodes[0].IP = ""
			},
			wantErr: "static node worker-0 ip is required",
		},
		{
			name: "missing nodes",
			mutate: func(cluster *v1.StaticNodeCluster) {
				cluster.Spec.Nodes = nil
			},
			wantErr: "static node cluster spec.nodes is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			tt.mutate(cluster)

			_, err := (&StaticNodeClusterReconciler{}).BuildDesiredNodes(context.Background(), cluster, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestStaticNodeClusterReconcilerBuildsDiscoverySafeNodesBeforeAcceleratorStatus(t *testing.T) {
	cluster := testStaticNodeCluster()

	nodes, err := (&StaticNodeClusterReconciler{}).BuildDesiredNodes(context.Background(), cluster, nil)

	require.NoError(t, err)
	require.Len(t, nodes, 2)

	for _, node := range nodes {
		require.NotNil(t, node.Spec)
		assert.Equal(t, "static-a", node.Spec.Cluster)
		assert.NotEmpty(t, node.Spec.IP)
		assert.NotEmpty(t, node.Spec.SSHAuthRef)
		assert.Empty(t, node.Spec.Components)
		if assert.NotNil(t, node.Spec.Warm) {
			assert.Empty(t, node.Spec.Warm.Images)
		}
	}
}

func TestStaticNodeClusterReconcilerPlansRayRecreateUpgradeOrder(t *testing.T) {
	tests := []struct {
		name            string
		step            string
		wantHeadImage   string
		wantWorkerImage string
		wantWorkerRay   bool
	}{
		{
			name:          "stopping workers keeps head on observed version",
			step:          "StoppingWorkers",
			wantHeadImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			wantWorkerRay: false,
		},
		{
			name:          "starting head keeps workers stopped",
			step:          "StartingHead",
			wantHeadImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
			wantWorkerRay: false,
		},
		{
			name:            "starting workers updates workers after head",
			step:            "StartingWorkers",
			wantHeadImage:   "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
			wantWorkerImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
			wantWorkerRay:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			cluster.Spec.Version = "v1.2.1"
			cluster.Status = &v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseUpgrading,
				Version:      "v1.2.0",
				ErrorMessage: tt.step,
			}
			currentNodes := staticNodeUpgradeCurrentNodes()

			plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

			require.NoError(t, err)
			assert.Equal(t, v1.StaticNodeClusterPhaseUpgrading, plan.Status.Phase)
			assert.Equal(t, "v1.2.0", plan.Status.Version)
			assert.Equal(t, tt.step, plan.Status.ErrorMessage)

			head := findStaticNode(plan.DesiredNodes, "head-0")
			require.NotNil(t, head)
			headRay := findComponent(head.Spec.Components, "ray-head")
			require.NotNil(t, headRay)
			assert.Equal(t, tt.wantHeadImage, headRay.Image)

			worker := findStaticNode(plan.DesiredNodes, "worker-0")
			require.NotNil(t, worker)
			workerRay := findComponent(worker.Spec.Components, "ray-worker")
			if !tt.wantWorkerRay {
				assert.Nil(t, workerRay)
			} else if assert.NotNil(t, workerRay) {
				assert.Equal(t, tt.wantWorkerImage, workerRay.Image)
			}
		})
	}
}

func TestStaticNodeClusterReconcilerAdvancesRayRecreateUpgradeStep(t *testing.T) {
	tests := []struct {
		name     string
		step     string
		mutate   func([]*v1.StaticNode)
		wantStep string
	}{
		{
			name:     "warm ready advances to stopping workers",
			step:     "Warming",
			wantStep: "StoppingWorkers",
		},
		{
			name: "workers stopped advances to starting head",
			step: "StoppingWorkers",
			mutate: func(nodes []*v1.StaticNode) {
				worker := findStaticNode(nodes, "worker-0")
				require.NotNil(t, worker)
				worker.Status.Components = []v1.NodeComponentStatus{
					{Name: "ray-worker", Type: v1.NodeComponentTypeRayWorker, Phase: v1.NodeComponentPhaseStopped},
				}
			},
			wantStep: "StartingHead",
		},
		{
			name: "target head running advances to starting workers",
			step: "StartingHead",
			mutate: func(nodes []*v1.StaticNode) {
				head := findStaticNode(nodes, "head-0")
				require.NotNil(t, head)
				head.Status.Components = []v1.NodeComponentStatus{
					{
						Name:          "ray-head",
						Type:          v1.NodeComponentTypeRayHead,
						Ready:         true,
						Phase:         v1.NodeComponentPhaseRunning,
						ObservedImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
					},
				}
			},
			wantStep: "StartingWorkers",
		},
		{
			name: "target workers running advances to verifying",
			step: "StartingWorkers",
			mutate: func(nodes []*v1.StaticNode) {
				head := findStaticNode(nodes, "head-0")
				require.NotNil(t, head)
				head.Status.Components = []v1.NodeComponentStatus{
					{
						Name:          "ray-head",
						Type:          v1.NodeComponentTypeRayHead,
						Ready:         true,
						Phase:         v1.NodeComponentPhaseRunning,
						ObservedImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
					},
				}

				worker := findStaticNode(nodes, "worker-0")
				require.NotNil(t, worker)
				worker.Status.Components = []v1.NodeComponentStatus{
					{
						Name:          "ray-worker",
						Type:          v1.NodeComponentTypeRayWorker,
						Ready:         true,
						Phase:         v1.NodeComponentPhaseRunning,
						ObservedImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
					},
				}
			},
			wantStep: "Verifying",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			cluster.Spec.Version = "v1.2.1"
			cluster.Status = &v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseUpgrading,
				Version:      "v1.2.0",
				ErrorMessage: tt.step,
			}
			currentNodes := staticNodeUpgradeCurrentNodes()
			if tt.mutate != nil {
				tt.mutate(currentNodes)
			}

			plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

			require.NoError(t, err)
			assert.Equal(t, v1.StaticNodeClusterPhaseUpgrading, plan.Status.Phase)
			assert.Equal(t, tt.wantStep, plan.Status.ErrorMessage)
		})
	}
}

func TestStaticNodeClusterReconcilerCompletesRayRecreateUpgradeWhenTargetReady(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseUpgrading,
		Version:      "v1.2.0",
		ErrorMessage: "Verifying",
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	targetImage := "registry.example.com/neutree/neutree/neutree-serve:v1.2.1"
	markStaticNodeUpgradeReady(currentNodes, targetImage)

	plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, plan.Status.Phase)
	assert.Equal(t, "v1.2.1", plan.Status.Version)
	assert.Empty(t, plan.Status.ErrorMessage)
}

func TestStaticNodeClusterReconcilerKeepsReadyWhenObservedVersionMatchesSpec(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase:   v1.StaticNodeClusterPhaseReady,
		Version: "v1.2.1",
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	markStaticNodeUpgradeReady(currentNodes, buildRayRuntimeImage(cluster))

	plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, plan.Status.Phase)
	assert.Equal(t, "v1.2.1", plan.Status.Version)
	assert.Empty(t, plan.Status.ErrorMessage)
}

func TestStaticNodeClusterReconcilerFallsBackToCPURuntimeWhenRuntimeProfileUnsupported(t *testing.T) {
	cluster := testStaticNodeCluster()
	currentNodes := []*v1.StaticNode{
		staticNodeWithAcceleratorStatus("head-0", v1.StaticNodeRoleHead, unsupportedNvidiaAcceleratorStatus()),
		staticNodeWithAcceleratorStatus("worker-0", v1.StaticNodeRoleWorker, unsupportedNvidiaAcceleratorStatus()),
	}

	plan, err := (&StaticNodeClusterReconciler{
		RuntimeProfileProvider: fakeRuntimeProfileProvider{profiles: map[string]*v1.AcceleratorProfile{}},
	}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	assert.Contains(t, plan.Status.ErrorMessage, `static node head-0 accelerator runtime profile "nvidia-unknown" is not supported; fallback to CPU runtime`)
	assert.Contains(t, plan.Status.ErrorMessage, `static node worker-0 accelerator runtime profile "nvidia-unknown" is not supported; fallback to CPU runtime`)
	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), currentNodes[0].Status.Accelerator.Type)
	assert.Equal(t, "nvidia-unknown", currentNodes[0].Status.Accelerator.ProductModel)

	head := findStaticNode(plan.DesiredNodes, "head-0")
	require.NotNil(t, head)
	headRay := findComponent(head.Spec.Components, "ray-head")
	require.NotNil(t, headRay)
	assert.NotContains(t, headRay.DockerRunOptions, "--runtime=nvidia")
	assert.NotContains(t, headRay.DockerRunOptions, "--gpus all")
	assert.Nil(t, findComponent(head.Spec.Components, acceleratorExporterComponentName))
}

func TestStaticNodeClusterReconcilerAggregateStatus(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []*v1.StaticNode
		wantStatus v1.StaticNodeClusterStatus
	}{
		{
			name: "ready when all nodes, warm, and metrics are ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
					readyComponent(vmagentComponentName),
				}),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseReady,
				DesiredNodes: 2,
				ReadyNodes:   2,
				HeadReady:    true,
				MetricsReady: true,
				WarmReady:    true,
				Version:      "v1.2.0",
			},
		},
		{
			name: "degraded when head is ready but a worker is not ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
					readyComponent(vmagentComponentName),
				}),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReconciling, false, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseDegraded,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    true,
				MetricsReady: false,
				WarmReady:    false,
				ErrorMessage: "static node worker-0 phase=Reconciling",
			},
		},
		{
			name: "failed when any node failed",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseFailed, false, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseFailed,
				DesiredNodes: 2,
				ReadyNodes:   0,
				HeadReady:    false,
				MetricsReady: false,
				WarmReady:    false,
				ErrorMessage: "static node head-0 phase=Failed; static node worker-0 is missing",
			},
		},
		{
			name: "failed node error message is aggregated",
			nodes: []*v1.StaticNode{
				staticNodeStatusWithError("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseFailed, "ssh connection failed"),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseFailed,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    false,
				MetricsReady: false,
				WarmReady:    false,
				ErrorMessage: "static node head-0 phase=Failed: ssh connection failed",
			},
		},
		{
			name: "ignores stale nodes and marks missing desired nodes not ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
				staticNodeStatus("stale-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
				}),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    false,
				MetricsReady: false,
				WarmReady:    false,
				ErrorMessage: "static node head-0 is missing",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := (&StaticNodeClusterReconciler{}).AggregateStatus(testStaticNodeCluster(), tt.nodes)

			assert.Equal(t, tt.wantStatus, status)
		})
	}
}

func TestStaticNodeClusterReconcilerAggregateStatusRecordsObservedVersionWhenReady(t *testing.T) {
	cluster := testStaticNodeCluster()
	nodes := []*v1.StaticNode{
		staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
			readyComponent(nodeExporterComponentName),
			readyComponent(vmagentComponentName),
		}),
		staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
			readyComponent(nodeExporterComponentName),
		}),
	}

	status := (&StaticNodeClusterReconciler{}).AggregateStatus(cluster, nodes)

	assert.Equal(t, v1.StaticNodeClusterPhaseReady, status.Phase)
	assert.Equal(t, "v1.2.0", status.Version)
	assert.Empty(t, status.ErrorMessage)
}

func testStaticNodeCluster() *v1.StaticNodeCluster {
	return &v1.StaticNodeCluster{
		Metadata: &v1.Metadata{
			Workspace:   "default",
			Name:        "static-a",
			Annotations: map[string]string{"source": "unit-test"},
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:       "v1.2.0",
			ImageRegistry: "registry.example.com/neutree",
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{
					Name:       "worker-0",
					IP:         "10.0.0.11",
					Role:       v1.StaticNodeRoleWorker,
					SSHAuthRef: "ssh-ref",
					SSHAuth:    &v1.Auth{SSHUser: "ray", SSHPrivateKey: "/tmp/key"},
				},
				{
					Name:       "head-0",
					IP:         "10.0.0.10",
					Role:       v1.StaticNodeRoleHead,
					SSHAuthRef: "ssh-ref",
					SSHAuth:    &v1.Auth{SSHUser: "ray", SSHPrivateKey: "/tmp/key"},
				},
			},
			MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
		},
	}
}

func staticNodeStatus(
	name string,
	role v1.StaticNodeRole,
	phase v1.StaticNodePhase,
	warmReady bool,
	components []v1.NodeComponentStatus,
) *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{Name: name},
		Spec:     &v1.StaticNodeSpec{Role: role},
		Status: &v1.StaticNodeStatus{
			Phase:      phase,
			Warm:       &v1.WarmStatus{Ready: warmReady},
			Components: components,
		},
	}
}

func staticNodeStatusWithError(
	name string,
	role v1.StaticNodeRole,
	phase v1.StaticNodePhase,
	message string,
) *v1.StaticNode {
	node := staticNodeStatus(name, role, phase, false, nil)
	node.Status.ErrorMessage = message

	return node
}

func staticNodeStatusWithAccelerator(
	name string,
	role v1.StaticNodeRole,
	phase v1.StaticNodePhase,
	warmReady bool,
	accelerator v1.StaticNodeAcceleratorStatus,
	components []v1.NodeComponentStatus,
) *v1.StaticNode {
	node := staticNodeStatus(name, role, phase, warmReady, components)
	node.Status.Accelerator = &accelerator

	return node
}

func nvidiaAcceleratorStatus() v1.StaticNodeAcceleratorStatus {
	return v1.StaticNodeAcceleratorStatus{
		Type:         v1.AcceleratorTypeNVIDIAGPU.String(),
		Vendor:       "nvidia",
		ProductName:  "NVIDIA GPU",
		ProductModel: "nvidia_gpu",
		Devices: []v1.StaticNodeAcceleratorDeviceStatus{
			{ID: "0", ProductName: "NVIDIA GPU", Healthy: true},
		},
	}
}

func cpuAcceleratorStatus() v1.StaticNodeAcceleratorStatus {
	return v1.CPUStaticNodeAcceleratorStatus()
}

func unsupportedNvidiaAcceleratorStatus() v1.StaticNodeAcceleratorStatus {
	status := nvidiaAcceleratorStatus()
	status.ProductModel = "nvidia-unknown"

	return status
}

func staticNodeWithAcceleratorStatus(
	name string,
	role v1.StaticNodeRole,
	accelerator v1.StaticNodeAcceleratorStatus,
) *v1.StaticNode {
	return &v1.StaticNode{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.StaticNodeSpec{
			Role: role,
		},
		Status: &v1.StaticNodeStatus{
			Accelerator: &accelerator,
			Warm:        &v1.WarmStatus{Ready: true},
		},
	}
}

type fakeRuntimeProfileProvider struct {
	profiles map[string]*v1.AcceleratorProfile
}

func (f fakeRuntimeProfileProvider) RuntimeProfile(
	_ context.Context,
	accelerator v1.StaticNodeAcceleratorStatus,
) (*v1.AcceleratorProfile, bool, error) {
	key := accelerator.ProductModel
	if key == "" {
		key = accelerator.Type
	}

	profile, ok := f.profiles[key]

	return profile, ok, nil
}

func readyComponent(name string) v1.NodeComponentStatus {
	return v1.NodeComponentStatus{
		Name:  name,
		Ready: true,
		Phase: v1.NodeComponentPhaseRunning,
	}
}

func assertNodeComponentTypes(t *testing.T, components []v1.NodeComponentSpec, want []v1.NodeComponentType) {
	t.Helper()

	require.Len(t, components, len(want))
	for i, component := range components {
		assert.Equal(t, want[i], component.Type)
	}
}

func findComponent(components []v1.NodeComponentSpec, name string) *v1.NodeComponentSpec {
	for i := range components {
		if components[i].Name == name {
			return &components[i]
		}
	}

	return nil
}

func findStaticNode(nodes []*v1.StaticNode, name string) *v1.StaticNode {
	for _, node := range nodes {
		if node != nil && node.Metadata != nil && node.Metadata.Name == name {
			return node
		}
	}

	return nil
}

func findConfigFile(configFiles []v1.NodeComponentConfigFile, path string) *v1.NodeComponentConfigFile {
	for i := range configFiles {
		if configFiles[i].Path == path {
			return &configFiles[i]
		}
	}

	return nil
}

func assertWarmImages(t *testing.T, images []v1.WarmImageSpec, want map[string]string) {
	t.Helper()

	require.Len(t, images, len(want))
	for name, ref := range want {
		assert.Equal(t, ref, warmImageRef(images, name))
	}
}

func warmImageRef(images []v1.WarmImageSpec, name string) string {
	for _, image := range images {
		if image.Name == name {
			return image.Ref
		}
	}

	return ""
}

func staticNodeUpgradeCurrentNodes() []*v1.StaticNode {
	oldRayImage := "registry.example.com/neutree/neutree/neutree-serve:v1.2.0"
	headRay := v1.NodeComponentSpec{
		Name:  "ray-head",
		Type:  v1.NodeComponentTypeRayHead,
		Image: oldRayImage,
	}
	workerRay := v1.NodeComponentSpec{
		Name:  "ray-worker",
		Type:  v1.NodeComponentTypeRayWorker,
		Image: oldRayImage,
	}

	return []*v1.StaticNode{
		{
			Metadata: &v1.Metadata{Name: "head-0"},
			Spec: &v1.StaticNodeSpec{
				Role:       v1.StaticNodeRoleHead,
				Components: []v1.NodeComponentSpec{headRay},
			},
			Status: &v1.StaticNodeStatus{
				Phase:       v1.StaticNodePhaseReady,
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU, ProductModel: v1.StaticNodeAcceleratorTypeCPU},
				Warm:        &v1.WarmStatus{Ready: true},
				Components: []v1.NodeComponentStatus{
					{Name: "ray-head", Type: v1.NodeComponentTypeRayHead, Ready: true, Phase: v1.NodeComponentPhaseRunning},
				},
			},
		},
		{
			Metadata: &v1.Metadata{Name: "worker-0"},
			Spec: &v1.StaticNodeSpec{
				Role:       v1.StaticNodeRoleWorker,
				Components: []v1.NodeComponentSpec{workerRay},
			},
			Status: &v1.StaticNodeStatus{
				Phase:       v1.StaticNodePhaseReady,
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU, ProductModel: v1.StaticNodeAcceleratorTypeCPU},
				Warm:        &v1.WarmStatus{Ready: true},
				Components: []v1.NodeComponentStatus{
					{Name: "ray-worker", Type: v1.NodeComponentTypeRayWorker, Ready: true, Phase: v1.NodeComponentPhaseRunning},
				},
			},
		},
	}
}

func markStaticNodeUpgradeReady(nodes []*v1.StaticNode, rayImage string) {
	head := findStaticNode(nodes, "head-0")
	if head != nil {
		head.Spec.Components = append(head.Spec.Components,
			v1.NodeComponentSpec{Name: nodeExporterComponentName, Type: v1.NodeComponentTypeNodeExporter},
			v1.NodeComponentSpec{Name: vmagentComponentName, Type: v1.NodeComponentTypeMetricsAgent},
		)
		head.Status.Components = []v1.NodeComponentStatus{
			{Name: "ray-head", Type: v1.NodeComponentTypeRayHead, Ready: true, Phase: v1.NodeComponentPhaseRunning, ObservedImage: rayImage},
			{Name: nodeExporterComponentName, Type: v1.NodeComponentTypeNodeExporter, Ready: true, Phase: v1.NodeComponentPhaseRunning},
			{Name: vmagentComponentName, Type: v1.NodeComponentTypeMetricsAgent, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		}
	}

	worker := findStaticNode(nodes, "worker-0")
	if worker != nil {
		worker.Spec.Components = append(worker.Spec.Components,
			v1.NodeComponentSpec{Name: nodeExporterComponentName, Type: v1.NodeComponentTypeNodeExporter},
		)
		worker.Status.Components = []v1.NodeComponentStatus{
			{Name: "ray-worker", Type: v1.NodeComponentTypeRayWorker, Ready: true, Phase: v1.NodeComponentPhaseRunning, ObservedImage: rayImage},
			{Name: nodeExporterComponentName, Type: v1.NodeComponentTypeNodeExporter, Ready: true, Phase: v1.NodeComponentPhaseRunning},
		}
	}
}
