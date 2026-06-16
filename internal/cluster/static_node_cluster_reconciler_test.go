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
					Volumes: []v1.NodeComponentVolume{
						{
							Name:      "dcgm-counters",
							HostPath:  "/etc/neutree/dcgm-exporter/default-counters.csv",
							MountPath: "/etc/neutree/dcgm-exporter/default-counters.csv",
							ReadOnly:  true,
						},
					},
					DockerRunOptions: []string{"--gpus all", "--cap-add=SYS_ADMIN"},
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
	assert.Contains(t, rayHead.Args[0], "for i in $(seq 1 30); do docker rm -f ray_container")
	assert.Less(t,
		strings.Index(rayHead.Args[0], "python /home/ray/start.py --head"),
		strings.LastIndex(rayHead.Args[0], "docker rm -f ray_container"),
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
		vmagentComponentName:             "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0",
	})
	assertNodeComponentTypes(t, head.Spec.Components, []v1.NodeComponentType{
		v1.NodeComponentTypeRayHead,
		v1.NodeComponentTypeNodeExporter,
		v1.NodeComponentTypeAcceleratorExporter,
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
	assert.Equal(t, []string{"--gpus all", "--cap-add=SYS_ADMIN"}, exporter.DockerRunOptions)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature.", exporter.ConfigFiles[0].Content)
	assert.Equal(t, "/etc/neutree/dcgm-exporter/default-counters.csv", exporter.Volumes[0].MountPath)
	assert.Equal(t, 9400, exporter.Ports[0].Port)
	require.NotNil(t, exporter.HealthCheck)
	assert.Equal(t, "/dcgm/metrics", exporter.HealthCheck.HTTPPath)

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
	acceleratorTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/accelerator-exporter-dcgm-metrics.json")
	require.NotNil(t, acceleratorTargets)
	assert.True(t, acceleratorTargets.SkipRestartOnChange)
	assert.Contains(t, acceleratorTargets.Content, `"10.0.0.10:9400"`)
	assert.NotContains(t, acceleratorTargets.Content, `"10.0.0.11:9400"`)
	assert.Contains(t, acceleratorTargets.Content, `"accelerator_type": "nvidia_gpu"`)
	assert.Contains(t, acceleratorTargets.Content, `"accelerator_exporter": "dcgm-exporter"`)

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
	assert.Contains(t, rayWorker.Args[0], "for i in $(seq 1 30); do docker rm -f ray_container")
	assert.Less(t,
		strings.Index(rayWorker.Args[0], "python /home/ray/start.py --address=10.0.0.10:6379"),
		strings.LastIndex(rayWorker.Args[0], "docker rm -f ray_container"),
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
	})
	assertWarmImages(t, worker.Spec.Warm.Images, map[string]string{
		"ray-runtime":             "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
		nodeExporterComponentName: "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
	})

	cluster.Spec.Version = "mutated"
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-serve:v1.2.0", warmImageRef(head.Spec.Warm.Images, "ray-runtime"))
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
				cluster.Spec.Head.NodeName = "missing"
			},
			wantErr: "head node missing not found",
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
		step            v1.StaticNodeClusterUpgradeStep
		wantPhase       v1.StaticNodeClusterPhase
		wantHeadImage   string
		wantWorkerImage string
		wantWorkerPhase v1.NodeComponentPhase
	}{
		{
			name:            "stopping workers keeps head on observed version",
			step:            v1.StaticNodeClusterUpgradeStepStoppingWorkers,
			wantPhase:       v1.StaticNodeClusterPhaseStopping,
			wantHeadImage:   "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			wantWorkerImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			wantWorkerPhase: v1.NodeComponentPhaseStopped,
		},
		{
			name:            "starting head keeps workers stopped",
			step:            v1.StaticNodeClusterUpgradeStepStartingHead,
			wantPhase:       v1.StaticNodeClusterPhaseStarting,
			wantHeadImage:   "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
			wantWorkerImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			wantWorkerPhase: v1.NodeComponentPhaseStopped,
		},
		{
			name:            "starting workers updates workers after head",
			step:            v1.StaticNodeClusterUpgradeStepStartingWorkers,
			wantPhase:       v1.StaticNodeClusterPhaseStarting,
			wantHeadImage:   "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
			wantWorkerImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			cluster.Spec.Version = "v1.2.1"
			cluster.Status = &v1.StaticNodeClusterStatus{
				Phase: v1.StaticNodeClusterPhaseReady,
				Upgrade: &v1.StaticNodeClusterUpgradeStatus{
					ObservedVersion: "v1.2.0",
					TargetVersion:   "v1.2.1",
					Step:            tt.step,
				},
			}
			currentNodes := staticNodeUpgradeCurrentNodes()

			plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

			require.NoError(t, err)
			assert.Equal(t, tt.wantPhase, plan.Status.Phase)
			require.NotNil(t, plan.Status.Upgrade)
			assert.Equal(t, "v1.2.1", plan.Status.Upgrade.TargetVersion)
			assert.Equal(t, tt.step, plan.Status.Upgrade.Step)

			head := findStaticNode(plan.DesiredNodes, "head-0")
			require.NotNil(t, head)
			headRay := findComponent(head.Spec.Components, "ray-head")
			require.NotNil(t, headRay)
			assert.Equal(t, tt.wantHeadImage, headRay.Image)

			worker := findStaticNode(plan.DesiredNodes, "worker-0")
			require.NotNil(t, worker)
			workerRay := findComponent(worker.Spec.Components, "ray-worker")
			require.NotNil(t, workerRay)
			assert.Equal(t, tt.wantWorkerImage, workerRay.Image)
			assert.Equal(t, tt.wantWorkerPhase, workerRay.DesiredPhase)
		})
	}
}

func TestStaticNodeClusterReconcilerAdvancesRayRecreateUpgradeStep(t *testing.T) {
	tests := []struct {
		name     string
		step     v1.StaticNodeClusterUpgradeStep
		mutate   func([]*v1.StaticNode)
		wantStep v1.StaticNodeClusterUpgradeStep
	}{
		{
			name:     "warm ready advances to stopping workers",
			step:     v1.StaticNodeClusterUpgradeStepWarming,
			wantStep: v1.StaticNodeClusterUpgradeStepStoppingWorkers,
		},
		{
			name: "workers stopped advances to starting head",
			step: v1.StaticNodeClusterUpgradeStepStoppingWorkers,
			mutate: func(nodes []*v1.StaticNode) {
				worker := findStaticNode(nodes, "worker-0")
				require.NotNil(t, worker)
				worker.Status.Components = []v1.NodeComponentStatus{
					{Name: "ray-worker", Type: v1.NodeComponentTypeRayWorker, Phase: v1.NodeComponentPhaseStopped},
				}
			},
			wantStep: v1.StaticNodeClusterUpgradeStepStartingHead,
		},
		{
			name: "target head running advances to starting workers",
			step: v1.StaticNodeClusterUpgradeStepStartingHead,
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
			wantStep: v1.StaticNodeClusterUpgradeStepStartingWorkers,
		},
		{
			name: "target workers running advances to verifying",
			step: v1.StaticNodeClusterUpgradeStepStartingWorkers,
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
			wantStep: v1.StaticNodeClusterUpgradeStepVerifying,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			cluster.Spec.Version = "v1.2.1"
			cluster.Status = &v1.StaticNodeClusterStatus{
				Phase: v1.StaticNodeClusterPhaseReady,
				Upgrade: &v1.StaticNodeClusterUpgradeStatus{
					ObservedVersion: "v1.2.0",
					TargetVersion:   "v1.2.1",
					Step:            tt.step,
				},
			}
			currentNodes := staticNodeUpgradeCurrentNodes()
			if tt.mutate != nil {
				tt.mutate(currentNodes)
			}

			plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

			require.NoError(t, err)
			require.NotNil(t, plan.Status.Upgrade)
			assert.Equal(t, tt.wantStep, plan.Status.Upgrade.Step)
		})
	}
}

func TestStaticNodeClusterReconcilerCompletesRayRecreateUpgradeWhenTargetReady(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase: v1.StaticNodeClusterPhaseVerifying,
		Upgrade: &v1.StaticNodeClusterUpgradeStatus{
			ObservedVersion: "v1.2.0",
			TargetVersion:   "v1.2.1",
			Step:            v1.StaticNodeClusterUpgradeStepVerifying,
		},
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	targetImage := "registry.example.com/neutree/neutree/neutree-serve:v1.2.1"
	markStaticNodeUpgradeReady(currentNodes, targetImage)

	plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, plan.Status.Phase)
	require.NotNil(t, plan.Status.Upgrade)
	assert.Equal(t, "v1.2.1", plan.Status.Upgrade.ObservedVersion)
	assert.Empty(t, plan.Status.Upgrade.TargetVersion)
	assert.Empty(t, plan.Status.Upgrade.Step)
}

func TestStaticNodeClusterReconcilerKeepsReadyWhenObservedVersionMatchesSpec(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase: v1.StaticNodeClusterPhaseReady,
		Upgrade: &v1.StaticNodeClusterUpgradeStatus{
			ObservedVersion: "v1.2.1",
		},
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	markStaticNodeUpgradeReady(currentNodes, buildRayRuntimeImage(cluster))

	plan, err := (&StaticNodeClusterReconciler{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, plan.Status.Phase)
	require.NotNil(t, plan.Status.Upgrade)
	assert.Equal(t, "v1.2.1", plan.Status.Upgrade.ObservedVersion)
	assert.Empty(t, plan.Status.Upgrade.TargetVersion)
	assert.Empty(t, plan.Status.Upgrade.Step)
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
	assert.Equal(t, "nvidia-unknown", currentNodes[0].Status.Accelerator.RuntimeProfile)

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
				Upgrade: &v1.StaticNodeClusterUpgradeStatus{
					ObservedVersion: "v1.2.0",
				},
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
	require.NotNil(t, status.Upgrade)
	assert.Equal(t, "v1.2.0", status.Upgrade.ObservedVersion)
	assert.Empty(t, status.Upgrade.TargetVersion)
	assert.Empty(t, status.Upgrade.Step)
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
			Head: v1.StaticNodeClusterHeadSpec{
				NodeName: "head-0",
			},
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
					Role:       v1.StaticNodeRoleWorker,
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
		Type:           v1.AcceleratorTypeNVIDIAGPU.String(),
		Vendor:         "nvidia",
		ProductName:    "NVIDIA GPU",
		ProductModel:   "nvidia_gpu",
		RuntimeProfile: v1.AcceleratorTypeNVIDIAGPU.String(),
		ResourceName:   "GPU",
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
	status.RuntimeProfile = "nvidia-unknown"

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
	key := accelerator.RuntimeProfile
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
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU, RuntimeProfile: v1.StaticNodeAcceleratorTypeCPU},
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
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU, RuntimeProfile: v1.StaticNodeAcceleratorTypeCPU},
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
