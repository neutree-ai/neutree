package staticcluster

import (
	"context"
	"fmt"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlannerPlanBuildsDesiredNodes(t *testing.T) {
	cluster := testStaticNodeCluster()
	profiles := map[string]*v1.AcceleratorProfile{
		v1.AcceleratorTypeNVIDIAGPU.String(): {
			AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
			ClusterRuntime: &v1.RuntimeConfig{
				Runtime: "nvidia",
				Env: map[string]string{
					"ACCELERATOR_TYPE": "gpu",
				},
				Options: []string{"--gpus all", "--volume /cluster-only:/cluster-only:ro"},
			},
			MetricsExporter: &v1.AcceleratorExporterProfile{
				Name:  "dcgm-exporter",
				Image: "nvcr.io/nvidia/k8s/dcgm-exporter:test",
				Args:  []string{"--collectors", "/etc/neutree/dcgm-exporter/default-counters.csv"},
				Env: map[string]string{
					"NVIDIA_VISIBLE_DEVICES": "all",
				},
				Port: 19400,
				ConfigFiles: []v1.AcceleratorExporterConfigFile{
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
	planner := &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{profiles: profiles},
		MetricsRemoteWriteURL:      "http://vm:8480/insert/0/prometheus/",
	}

	nodes := plannedStaticNodes(t, planner, cluster, currentNodes)

	require.Len(t, nodes, 2)

	head := nodes[0]
	require.NotNil(t, head.Metadata)
	require.NotNil(t, head.Spec)
	assert.Equal(t, "head-0", head.Metadata.Name)
	assert.Equal(t, "default", head.Metadata.Workspace)
	assert.Equal(t, "static-a", head.Spec.Cluster)
	assert.Equal(t, v1.StaticNodeRoleHead, head.Spec.Role)
	assert.Equal(t, "10.0.0.10", head.Spec.IP)
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
	assert.NotContains(t, rayHead.Args[0], "ray_container")
	assert.NotContains(t, rayHead.Args[0], "while true")
	assert.Contains(t, rayHead.Args[0], "--block")
	assert.NotContains(t, rayHead.Args[0], "tail -f /dev/null")
	assert.Contains(t, rayHead.Args[0], "--dashboard-port=8265")
	assert.Contains(t, rayHead.Args[0], v1.NeutreeServingVersionLabel)
	assert.NotContains(t, rayHead.Args[0], "--autoscaling-config")
	require.NotNil(t, rayHead.HealthCheck)
	assert.Empty(t, rayHead.HealthCheck.HTTPHost)
	assert.Empty(t, rayHead.HealthCheck.HTTPPath)
	assert.Equal(t, v1.RayletMetricsPort, rayHead.HealthCheck.Port)
	assert.Equal(t, "/root/.docker", rayHead.Env["DOCKER_CONFIG"])
	assert.Equal(t, "gpu", rayHead.Env["ACCELERATOR_TYPE"])
	assert.Contains(t, rayHead.DockerRunOptions, "--runtime=nvidia")
	assert.Contains(t, rayHead.DockerRunOptions, "--gpus all")
	assert.Contains(t, rayHead.DockerRunOptions, "--volume /etc/neutree/docker:/root/.docker:ro")
	require.NotNil(t, head.Spec.Warm)
	assertWarmImages(t, head.Spec.Warm.Images, map[string]string{
		"ray-runtime":                    "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
		nodeExporterComponentName:        "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
		nodeAgentComponentName:           "registry.example.com/neutree/neutree/neutree-node-agent:v1.1.0-rc.1",
		acceleratorExporterComponentName: "registry.example.com/neutree/nvidia/k8s/dcgm-exporter:test",
		vmagentComponentName:             "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0",
	})
	assertNodeComponentNames(t, head.Spec.Components, []string{
		"ray-head",
		nodeExporterComponentName,
		acceleratorExporterComponentName,
		nodeAgentComponentName,
		vmagentComponentName,
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
	assert.Equal(t, map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}, exporter.Env)
	assert.Equal(t, []string{"--net=host", "--cap-add=SYS_ADMIN", "--gpus all"}, exporter.DockerRunOptions)
	assert.Equal(t, "DCGM_FI_DEV_GPU_TEMP, gauge, GPU temperature.", exporter.ConfigFiles[0].Content)
	assert.Equal(t, "/etc/neutree/dcgm-exporter/default-counters.csv", exporter.Volumes[0].MountPath)
	assert.Equal(t, 19400, exporter.Ports[0].Port)
	require.NotNil(t, exporter.HealthCheck)
	assert.Equal(t, "/metrics", exporter.HealthCheck.HTTPPath)
	nodeAgent := findComponent(head.Spec.Components, nodeAgentComponentName)
	require.NotNil(t, nodeAgent)
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-node-agent:v1.1.0-rc.1", nodeAgent.Image)
	assert.Contains(t, nodeAgent.Args, "--listen-address=:19101")
	assert.Contains(t, nodeAgent.Args, "--cluster-type=ray")
	assert.Contains(t, nodeAgent.Args, "--metrics-mode=managed")
	assert.Contains(t, nodeAgent.Args, "--ray-dashboard-url=http://10.0.0.10:8265")
	assert.NotContains(t, nodeAgent.Args, "--node-exporter-url=http://127.0.0.1:19100/metrics")
	assert.NotContains(t, nodeAgent.Args, "--accelerator-exporter-url=http://127.0.0.1:19400/metrics")
	assert.Contains(t, nodeAgent.Args, "--procfs-root=/host/proc")
	assert.Contains(t, nodeAgent.Args, "--cgroupfs-root=/host/sys/fs/cgroup")
	assert.Contains(t, nodeAgent.Args, "--node=head-0")
	assert.Contains(t, nodeAgent.Args, "--node-ip=10.0.0.10")
	assert.Equal(t, map[string]string{"NVIDIA_VISIBLE_DEVICES": "all"}, nodeAgent.Env)
	assert.NotContains(t, nodeAgent.Args, "--workspace=default")
	assert.NotContains(t, nodeAgent.Args, "--cluster=static-a")
	assert.NotContains(t, nodeAgent.Args, "--static-node-cluster=static-a")
	assert.NotContains(t, nodeAgent.Args, "--node-role=head")
	assert.Contains(t, nodeAgent.DockerRunOptions, "--net=host")
	assert.Contains(t, nodeAgent.DockerRunOptions, "--pid=host")
	assert.Contains(t, nodeAgent.DockerRunOptions, "--cgroupns=host")
	assert.Contains(t, nodeAgent.DockerRunOptions, "--cap-add=SYS_ADMIN")
	assert.Contains(t, nodeAgent.DockerRunOptions, "--gpus all")
	assert.NotContains(t, nodeAgent.DockerRunOptions, "--volume /cluster-only:/cluster-only:ro")
	requireVolume(t, nodeAgent, "host-proc", "/proc", "/host/proc")
	requireVolume(t, nodeAgent, "host-cgroup", "/sys/fs/cgroup", "/host/sys/fs/cgroup")
	require.Len(t, nodeAgent.Ports, 1)
	assert.Equal(t, 19101, nodeAgent.Ports[0].Port)
	require.NotNil(t, nodeAgent.HealthCheck)
	assert.Equal(t, "/health", nodeAgent.HealthCheck.HTTPPath)
	assert.Equal(t, 19101, nodeAgent.HealthCheck.Port)
	vmagentComponent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagentComponent)
	assert.Equal(t, "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0", vmagentComponent.Image)
	assert.Contains(t, vmagentComponent.Args, "-remoteWrite.url=http://vm:8480/insert/0/prometheus/")
	assert.NotEmpty(t, vmagentComponent.ConfigHash)
	vmagentConfig := findConfigFile(vmagentComponent.ConfigFiles, vmagentConfigPath)
	require.NotNil(t, vmagentConfig)
	assert.Contains(t, vmagentConfig.Content, `scrape_interval: 30s`)
	assert.Contains(t, vmagentConfig.Content, `scrape_timeout: 30s`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-node-exporter`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/node-exporter.json`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-node-agent`)
	assert.NotContains(t, vmagentConfig.Content, `honor_labels: true`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/node-agent.json`)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-ray`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/ray.json`)
	assert.Contains(t, vmagentConfig.Content, `metric_relabel_configs:`)
	assert.Contains(t, vmagentConfig.Content, `regex: 'ray_vllm[:_](.+)'`)
	assert.Contains(t, vmagentConfig.Content, `replacement: 'vllm:$1'`)
	assert.Contains(t, vmagentConfig.Content, `regex: 'ray_sglang[:_](.+)'`)
	assert.Contains(t, vmagentConfig.Content, `replacement: 'sglang:$1'`)
	assert.NotContains(t, vmagentConfig.Content, `replacement: 'sglang_$1'`)
	assert.NotContains(t, vmagentConfig.Content, `action: labeldrop`)
	assert.Contains(t, vmagentConfig.Content, `job_name: accelerator-exporter-nvidia-gpu`)
	assert.NotContains(t, vmagentConfig.Content, `metrics_path: "/dcgm/metrics"`)
	assert.Contains(t, vmagentConfig.Content, `/etc/neutree/vmagent/file_sd/accelerator-exporter-nvidia-gpu.json`)
	assert.NotContains(t, vmagentConfig.Content, `remote_write:`)
	nodeTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/node-exporter.json")
	require.NotNil(t, nodeTargets)
	assert.True(t, nodeTargets.SkipRestartOnChange)
	assert.Contains(t, nodeTargets.Content, `"10.0.0.10:19100"`)
	assert.Contains(t, nodeTargets.Content, `"10.0.0.11:19100"`)
	assert.Contains(t, nodeTargets.Content, `"static_node_cluster": "static-a"`)
	nodeAgentTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/node-agent.json")
	require.NotNil(t, nodeAgentTargets)
	assert.True(t, nodeAgentTargets.SkipRestartOnChange)
	assert.Contains(t, nodeAgentTargets.Content, `"10.0.0.10:19101"`)
	assert.Contains(t, nodeAgentTargets.Content, `"10.0.0.11:19101"`)
	rayTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/ray.json")
	require.NotNil(t, rayTargets)
	assert.True(t, rayTargets.SkipRestartOnChange)
	assert.Contains(t, rayTargets.Content, `"10.0.0.10:54311"`)
	assert.Contains(t, rayTargets.Content, `"10.0.0.11:54311"`)
	acceleratorTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/accelerator-exporter-nvidia-gpu.json")
	require.NotNil(t, acceleratorTargets)
	assert.True(t, acceleratorTargets.SkipRestartOnChange)
	assert.Contains(t, acceleratorTargets.Content, `"10.0.0.10:19400"`)
	assert.NotContains(t, acceleratorTargets.Content, `"10.0.0.11:19400"`)
	assert.Contains(t, acceleratorTargets.Content, `"accelerator_type": "nvidia_gpu"`)

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
	assert.NotContains(t, rayWorker.Args[0], "ray_container")
	assert.NotContains(t, rayWorker.Args[0], "while true")
	assert.Contains(t, rayWorker.Args[0], "--block")
	assert.NotContains(t, rayWorker.Args[0], "tail -f /dev/null")
	assert.Contains(t, rayWorker.Args[0], v1.StaticNodeProvisionType)
	require.NotNil(t, rayWorker.HealthCheck)
	assert.Empty(t, rayWorker.HealthCheck.HTTPHost)
	assert.Empty(t, rayWorker.HealthCheck.HTTPPath)
	assert.Equal(t, v1.RayletMetricsPort, rayWorker.HealthCheck.Port)
	assert.Equal(t, "/root/.docker", rayWorker.Env["DOCKER_CONFIG"])
	assert.Contains(t, rayWorker.DockerRunOptions, "--volume /etc/neutree/docker:/root/.docker:ro")
	assertNodeComponentNames(t, worker.Spec.Components, []string{"ray-worker", nodeExporterComponentName, nodeAgentComponentName})
	assertWarmImages(t, worker.Spec.Warm.Images, map[string]string{
		"ray-runtime":             "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
		nodeExporterComponentName: "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
		nodeAgentComponentName:    "registry.example.com/neutree/neutree/neutree-node-agent:v1.1.0-rc.1",
	})

	cluster.Spec.Version = "mutated"
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-serve:v1.2.0", warmImageRef(head.Spec.Warm.Images, "ray-runtime"))
}

func TestPlannerSkipsInvalidAcceleratorExporterProfiles(t *testing.T) {
	tests := []struct {
		name     string
		exporter *v1.AcceleratorExporterProfile
	}{
		{
			name: "empty name",
			exporter: &v1.AcceleratorExporterProfile{
				Image: "nvcr.io/nvidia/k8s/dcgm-exporter:test",
				Port:  9400,
			},
		},
		{
			name: "empty image",
			exporter: &v1.AcceleratorExporterProfile{
				Name: "dcgm-exporter",
				Port: 9400,
			},
		},
		{
			name: "invalid port",
			exporter: &v1.AcceleratorExporterProfile{
				Name:  "dcgm-exporter",
				Image: "nvcr.io/nvidia/k8s/dcgm-exporter:test",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
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

			nodes := plannedStaticNodes(t, &Planner{
				AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
					profiles: map[string]*v1.AcceleratorProfile{
						v1.AcceleratorTypeNVIDIAGPU.String(): {
							AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
							MetricsExporter: tt.exporter,
						},
					},
				},
				MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
			}, cluster, currentNodes)

			head := findStaticNode(nodes, "head-0")
			require.NotNil(t, head)
			assert.Nil(t, findComponent(head.Spec.Components, acceleratorExporterComponentName))
			assert.NotEqual(t, "", warmImageRef(head.Spec.Warm.Images, nodeExporterComponentName))
			assert.Equal(t, "", warmImageRef(head.Spec.Warm.Images, acceleratorExporterComponentName))
		})
	}
}

func TestPlannerIncludesMetricsComponentsForStaticFlowVersion(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.0.2"
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

	nodes := plannedStaticNodes(t, &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				v1.AcceleratorTypeNVIDIAGPU.String(): {
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
					MetricsExporter: &v1.AcceleratorExporterProfile{
						Name:  "dcgm-exporter",
						Image: "nvcr.io/nvidia/k8s/dcgm-exporter:test",
						Port:  19400,
					},
				},
			},
		},
		MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	assert.NotNil(t, findComponent(head.Spec.Components, nodeExporterComponentName))
	assert.NotNil(t, findComponent(head.Spec.Components, acceleratorExporterComponentName))
	assert.NotEqual(t, "", warmImageRef(head.Spec.Warm.Images, nodeExporterComponentName))
	assert.NotEqual(t, "", warmImageRef(head.Spec.Warm.Images, acceleratorExporterComponentName))

	vmagentComponent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagentComponent)
	vmagentConfig := findConfigFile(vmagentComponent.ConfigFiles, vmagentConfigPath)
	require.NotNil(t, vmagentConfig)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-node-exporter`)
	assert.Contains(t, vmagentConfig.Content, `job_name: accelerator-exporter-nvidia-gpu`)
	assert.NotNil(t, findConfigFile(vmagentComponent.ConfigFiles, vmagentNodeExporterFileSDPath))
}

func TestPlannerUsesExternalAcceleratorExporterTargets(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Metrics = &v1.ClusterMetricsConfig{
		AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
			Mode: v1.ClusterAcceleratorExporterModeExternal,
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

	nodes := plannedStaticNodes(t, &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				v1.AcceleratorTypeNVIDIAGPU.String(): {
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
					MetricsExporter: &v1.AcceleratorExporterProfile{
						Name:        "dcgm-exporter",
						Image:       "nvcr.io/nvidia/k8s/dcgm-exporter:test",
						Port:        19400,
						MetricsPath: "/dcgm/metrics",
					},
				},
			},
		},
		MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	assert.NotNil(t, findComponent(head.Spec.Components, nodeExporterComponentName))
	assert.Nil(t, findComponent(head.Spec.Components, acceleratorExporterComponentName))
	assert.Equal(t, "", warmImageRef(head.Spec.Warm.Images, acceleratorExporterComponentName))

	vmagentComponent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagentComponent)
	vmagentConfig := findConfigFile(vmagentComponent.ConfigFiles, vmagentConfigPath)
	require.NotNil(t, vmagentConfig)
	assert.Contains(t, vmagentConfig.Content, `job_name: static-node-accelerator-exporter`)
	assert.NotContains(t, vmagentConfig.Content, `metrics_path: "/dcgm/metrics"`)

	acceleratorTargets := findConfigFile(vmagentComponent.ConfigFiles, "/etc/neutree/vmagent/file_sd/accelerator-exporter.json")
	require.NotNil(t, acceleratorTargets)
	assert.Contains(t, acceleratorTargets.Content, `"10.0.0.10:9400"`)
	assert.NotContains(t, acceleratorTargets.Content, `"10.0.0.11:9400"`)
}

func TestPlannerSkipsMetricsComponentsWithoutValidRemoteWriteURL(t *testing.T) {
	tests := []struct {
		name                  string
		metricsRemoteWriteURL string
	}{
		{name: "empty url"},
		{name: "invalid url", metricsRemoteWriteURL: "vm:8480/insert/0/prometheus/"},
		{name: "unsupported scheme", metricsRemoteWriteURL: "ftp://vm/metrics"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := testStaticNodeCluster()
			currentNodes := []*v1.StaticNode{
				staticNodeStatusWithAccelerator(
					"head-0",
					v1.StaticNodeRoleHead,
					v1.StaticNodePhaseReady,
					true,
					cpuAcceleratorStatus(),
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

			nodes := plannedStaticNodes(t, &Planner{
				MetricsRemoteWriteURL: tt.metricsRemoteWriteURL,
			}, cluster, currentNodes)

			head := findStaticNode(nodes, "head-0")
			require.NotNil(t, head)
			assertNodeComponentNames(t, head.Spec.Components, []string{"ray-head"})
			assertWarmImages(t, head.Spec.Warm.Images, map[string]string{
				"ray-runtime": "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			})

			worker := findStaticNode(nodes, "worker-0")
			require.NotNil(t, worker)
			assertNodeComponentNames(t, worker.Spec.Components, []string{"ray-worker"})
			assertWarmImages(t, worker.Spec.Warm.Images, map[string]string{
				"ray-runtime": "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			})
		})
	}
}

func TestPlannerSkipsNodeExporterTargetsForUndiscoveredNodes(t *testing.T) {
	cluster := testStaticNodeCluster()
	currentNodes := []*v1.StaticNode{
		staticNodeStatusWithAccelerator(
			"head-0",
			v1.StaticNodeRoleHead,
			v1.StaticNodePhaseReady,
			true,
			cpuAcceleratorStatus(),
			nil,
		),
	}

	nodes := plannedStaticNodes(t, &Planner{
		MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	vmagentComponent := findComponent(head.Spec.Components, vmagentComponentName)
	require.NotNil(t, vmagentComponent)

	nodeTargets := findConfigFile(vmagentComponent.ConfigFiles, vmagentNodeExporterFileSDPath)
	require.NotNil(t, nodeTargets)
	assert.Contains(t, nodeTargets.Content, `"10.0.0.10:19100"`)
	assert.NotContains(t, nodeTargets.Content, `"10.0.0.11:19100"`)

	worker := findStaticNode(nodes, "worker-0")
	require.NotNil(t, worker)
	assert.Empty(t, worker.Spec.Components)
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
			image: "nvcr.io/nvidia/ray-runtime:test",
			want:  "registry.example.com/neutree/nvidia/ray-runtime:test",
		},
		{
			name:  "keeps docker hub repository path",
			image: "library/ray-runtime:v1.2.0",
			want:  "registry.example.com/neutree/library/ray-runtime:v1.2.0",
		},
		{
			name:  "strips localhost registry",
			image: "localhost:5000/custom/ray-runtime:v1",
			want:  "registry.example.com/neutree/custom/ray-runtime:v1",
		},
		{
			name:  "keeps digest",
			image: "quay.io/neutree/ray-runtime@sha256:abc",
			want:  "registry.example.com/neutree/neutree/ray-runtime@sha256:abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, staticComponentImage(cluster, tt.image))
		})
	}
}

func TestDefaultNodeAgentImageUsesSameRepositoryPathAsKubernetes(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v9.9.9"

	assert.Equal(t, "neutree/neutree-node-agent:v1.1.0-rc.1", defaultNodeAgentImage(cluster))
}

func TestPlannerUsesClusterRuntimeImageSuffix(t *testing.T) {
	cluster := testStaticNodeCluster()
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

	nodes := plannedStaticNodes(t, &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				v1.AcceleratorTypeNVIDIAGPU.String(): {
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
					ClusterRuntime: &v1.RuntimeConfig{
						ImageSuffix: "cuda",
						Runtime:     "nvidia",
					},
				},
			},
		},
		MetricsRemoteWriteURL: "http://vm:8480/insert/0/prometheus/",
	}, cluster, currentNodes)

	head := findStaticNode(nodes, "head-0")
	require.NotNil(t, head)
	rayHead := findComponent(head.Spec.Components, "ray-head")
	require.NotNil(t, rayHead)
	assert.Equal(t, "registry.example.com/neutree/neutree/neutree-serve:v1.2.0-cuda", rayHead.Image)
	assertWarmImages(t, head.Spec.Warm.Images, map[string]string{
		"ray-runtime":             "registry.example.com/neutree/neutree/neutree-serve:v1.2.0-cuda",
		nodeExporterComponentName: "registry.example.com/neutree/prometheus/node-exporter:v1.8.2",
		nodeAgentComponentName:    "registry.example.com/neutree/neutree/neutree-node-agent:v1.1.0-rc.1",
		vmagentComponentName:      "registry.example.com/neutree/victoriametrics/vmagent:v1.115.0",
	})
}

func TestPlannerPlanValidation(t *testing.T) {
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

			_, err := (&Planner{}).Plan(context.Background(), cluster, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestPlannerBuildsDiscoverySafeNodesBeforeAcceleratorStatus(t *testing.T) {
	cluster := testStaticNodeCluster()

	nodes := plannedStaticNodes(t, &Planner{}, cluster, nil)

	require.Len(t, nodes, 2)

	for _, node := range nodes {
		require.NotNil(t, node.Spec)
		assert.Equal(t, "static-a", node.Spec.Cluster)
		assert.NotEmpty(t, node.Spec.IP)
		assert.Empty(t, node.Spec.Components)
		if assert.NotNil(t, node.Spec.Warm) {
			assert.Empty(t, node.Spec.Warm.Images)
		}
	}
}

func TestPlannerPlanWaitsForDesiredComponents(t *testing.T) {
	cluster := testStaticNodeCluster()
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
	currentNodes[1].Status.Components = []v1.NodeComponentStatus{
		{
			Name:    "ray-worker",
			Phase:   v1.NodeComponentPhasePending,
			Reason:  "HeadNodePending",
			Message: "head static node is not ready",
		},
	}

	desiredNodePlans, err := (&Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				v1.AcceleratorTypeNVIDIAGPU.String(): {
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
					ClusterRuntime: &v1.RuntimeConfig{
						Runtime: "nvidia",
						Options: []string{"--gpus all"},
					},
				},
			},
		},
	}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Phase)
	assert.Contains(t, status.ErrorMessage, "static node head-0 component ray-head is not running desired config")
	assert.Contains(t, status.ErrorMessage, "static node worker-0 component ray-worker is not running desired config")
	assert.Contains(t, status.ErrorMessage, "phase=Pending")
	assert.Contains(t, status.ErrorMessage, "reason=HeadNodePending")
	assert.Contains(t, status.ErrorMessage, "message=head static node is not ready")
}

func TestPlannerPlansRayRecreateUpgradeOrder(t *testing.T) {
	tests := []struct {
		name            string
		mutate          func([]*v1.StaticNode)
		wantStep        string
		wantHeadImage   string
		wantWorkerImage string
		wantWorkerRay   bool
	}{
		{
			name:          "stopping workers keeps head on observed version",
			wantStep:      "StoppingWorkers",
			wantHeadImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.0",
			wantWorkerRay: false,
		},
		{
			name:          "starting head keeps workers stopped",
			mutate:        markUpgradeWorkersStopped,
			wantStep:      "StartingHead",
			wantHeadImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
			wantWorkerRay: false,
		},
		{
			name:            "starting workers updates workers after head",
			mutate:          markUpgradeHeadTargetRunning,
			wantStep:        "StartingWorkers",
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
				ErrorMessage: "stale status message is ignored",
			}
			currentNodes := staticNodeUpgradeCurrentNodes()
			if tt.mutate != nil {
				tt.mutate(currentNodes)
			}

			desiredNodePlans, err := (&Planner{}).Plan(context.Background(), cluster, currentNodes)

			require.NoError(t, err)
			status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
			assert.Equal(t, v1.StaticNodeClusterPhaseUpgrading, status.Phase)
			assert.Equal(t, "v1.2.0", status.Version)
			assert.Contains(t, status.ErrorMessage, tt.wantStep)

			desiredNodes := staticNodesFromPlans(desiredNodePlans)
			head := findStaticNode(desiredNodes, "head-0")
			require.NotNil(t, head)
			headRay := findComponent(head.Spec.Components, "ray-head")
			require.NotNil(t, headRay)
			assert.Equal(t, tt.wantHeadImage, headRay.Image)

			worker := findStaticNode(desiredNodes, "worker-0")
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

func TestPlannerAdvancesRayRecreateUpgradeStep(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func([]*v1.StaticNode)
		wantStep string
	}{
		{
			name:     "warm ready advances to stopping workers",
			wantStep: "StoppingWorkers",
		},
		{
			name:     "workers stopped advances to starting head",
			mutate:   markUpgradeWorkersStopped,
			wantStep: "StartingHead",
		},
		{
			name:     "target head running advances to starting workers",
			mutate:   markUpgradeHeadTargetRunning,
			wantStep: "StartingWorkers",
		},
		{
			name: "target workers running advances to verifying",
			mutate: func(nodes []*v1.StaticNode) {
				head := findStaticNode(nodes, "head-0")
				require.NotNil(t, head)
				head.Status.Components = []v1.NodeComponentStatus{
					{
						Name:          "ray-head",
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
				ErrorMessage: "stale status message is ignored",
			}
			currentNodes := staticNodeUpgradeCurrentNodes()
			if tt.mutate != nil {
				tt.mutate(currentNodes)
			}

			desiredNodePlans, err := (&Planner{}).Plan(context.Background(), cluster, currentNodes)

			require.NoError(t, err)
			status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
			assert.Equal(t, v1.StaticNodeClusterPhaseUpgrading, status.Phase)
			assert.Contains(t, status.ErrorMessage, tt.wantStep)
		})
	}
}

func TestPlannerCompletesRayRecreateUpgradeWhenTargetReady(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseUpgrading,
		Version:      "v1.2.0",
		ErrorMessage: "Verifying",
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	targetImage := "registry.example.com/neutree/neutree/neutree-serve:v1.2.1"
	markStaticNodeUpgradeReady(t, nil, cluster, currentNodes, targetImage)

	desiredNodePlans, err := (&Planner{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, status.Phase)
	assert.Equal(t, "v1.2.1", status.Version)
	assert.Empty(t, status.ErrorMessage)
}

func TestPlannerCompletesRayRecreateUpgradeWithImageSuffix(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseUpgrading,
		Version:      "v1.2.0",
		ErrorMessage: "Verifying",
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	for _, node := range currentNodes {
		node.Status.Accelerator = &v1.StaticNodeAcceleratorStatus{
			Type: v1.AcceleratorTypeNVIDIAGPU.String(),
		}
	}
	planner := &Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{
			profiles: map[string]*v1.AcceleratorProfile{
				v1.AcceleratorTypeNVIDIAGPU.String(): {
					AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
					ClusterRuntime:  &v1.RuntimeConfig{ImageSuffix: "cuda"},
				},
			},
		},
	}
	markStaticNodeUpgradeReady(t, planner, cluster, currentNodes, buildRayRuntimeImage(cluster, "cuda"))

	desiredNodePlans, err := planner.Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, status.Phase)
	assert.Equal(t, "v1.2.1", status.Version)
	assert.Empty(t, status.ErrorMessage)
}

func TestPlannerFailsUpgradeWhenNodeFails(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseUpgrading,
		Version:      "v1.2.0",
		ErrorMessage: "Warming",
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	head := findStaticNode(currentNodes, "head-0")
	require.NotNil(t, head)
	head.Status.Phase = v1.StaticNodePhaseFailed
	head.Status.ErrorMessage = "ssh connection failed"

	desiredNodePlans, err := (&Planner{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
	assert.Equal(t, v1.StaticNodeClusterPhaseFailed, status.Phase)
	assert.Equal(t, "v1.2.0", status.Version)
	assert.Contains(t, status.ErrorMessage, "StoppingWorkers")
	assert.Contains(t, status.ErrorMessage, "static node head-0 phase=Failed:\nssh connection failed")
}

func TestPlannerKeepsReadyWhenObservedVersionMatchesSpec(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Version = "v1.2.1"
	cluster.Status = &v1.StaticNodeClusterStatus{
		Phase:   v1.StaticNodeClusterPhaseReady,
		Version: "v1.2.1",
	}
	currentNodes := staticNodeUpgradeCurrentNodes()
	markStaticNodeUpgradeReady(t, nil, cluster, currentNodes, buildRayRuntimeImage(cluster))

	desiredNodePlans, err := (&Planner{}).Plan(context.Background(), cluster, currentNodes)

	require.NoError(t, err)
	status := (StatusAggregator{}).Aggregate(cluster, currentNodes, desiredNodePlans)
	assert.Equal(t, v1.StaticNodeClusterPhaseReady, status.Phase)
	assert.Equal(t, "v1.2.1", status.Version)
	assert.Empty(t, status.ErrorMessage)
}

func TestPlannerReturnsErrorWhenAcceleratorProfileMissing(t *testing.T) {
	cluster := testStaticNodeCluster()
	currentNodes := []*v1.StaticNode{
		staticNodeWithAcceleratorStatus("head-0", v1.StaticNodeRoleHead, nvidiaAcceleratorStatus()),
		staticNodeWithAcceleratorStatus("worker-0", v1.StaticNodeRoleWorker, nvidiaAcceleratorStatus()),
	}

	desiredNodePlans, err := (&Planner{
		AcceleratorProfileProvider: fakeAcceleratorProfileProvider{profiles: map[string]*v1.AcceleratorProfile{}},
	}).Plan(context.Background(), cluster, currentNodes)

	require.Error(t, err)
	assert.Nil(t, desiredNodePlans)
	assert.Contains(t, err.Error(), "accelerator profile nvidia_gpu not found")
}

func TestStatusAggregatorAggregate(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []*v1.StaticNode
		wantStatus v1.StaticNodeClusterStatus
	}{
		{
			name: "ready when all nodes and warm are ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, nil),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseReady,
				DesiredNodes: 2,
				ReadyNodes:   2,
				HeadReady:    true,
				WarmReady:    true,
				Version:      "v1.2.0",
			},
		},
		{
			name: "provisioning when head is ready but a worker is not ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, nil),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReconciling, false, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    true,
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
				WarmReady:    false,
				ErrorMessage: "static node head-0 phase=Failed\nstatic node worker-0 is missing",
			},
		},
		{
			name: "failed node error message is aggregated",
			nodes: []*v1.StaticNode{
				staticNodeStatusWithError("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseFailed, "ssh connection failed"),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseFailed,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    false,
				WarmReady:    false,
				ErrorMessage: "static node head-0 phase=Failed:\nssh connection failed",
			},
		},
		{
			name: "marks stale and missing nodes as not ready",
			nodes: []*v1.StaticNode{
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
				staticNodeStatus("stale-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    false,
				WarmReady:    false,
				ErrorMessage: "stale static node stale-0 exists\nstatic node head-0 is missing",
			},
		},
		{
			name: "reports stale deleting node during worker removal",
			nodes: []*v1.StaticNode{
				staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, nil),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
				func() *v1.StaticNode {
					node := staticNodeStatus("stale-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil)
					node.Metadata.DeletionTimestamp = "2026-07-02T00:00:00Z"

					return node
				}(),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 2,
				ReadyNodes:   2,
				HeadReady:    true,
				WarmReady:    true,
				ErrorMessage: "stale static node stale-0 is deleting",
			},
		},
		{
			name: "ignores stale error message from ready desired node",
			nodes: []*v1.StaticNode{
				func() *v1.StaticNode {
					node := staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, nil)
					node.Status.ErrorMessage = "previous failure"

					return node
				}(),
				staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReconciling, false, nil),
			},
			wantStatus: v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 2,
				ReadyNodes:   1,
				HeadReady:    true,
				WarmReady:    false,
				ErrorMessage: "static node worker-0 phase=Reconciling",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := (StatusAggregator{}).Aggregate(testStaticNodeCluster(), tt.nodes, nil)

			assert.Equal(t, tt.wantStatus, status)
		})
	}
}

func TestStatusAggregatorRecordsObservedVersionWhenReady(t *testing.T) {
	cluster := testStaticNodeCluster()
	nodes := []*v1.StaticNode{
		staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, nil),
		staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
	}

	status := (StatusAggregator{}).Aggregate(cluster, nodes, nil)

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
					Name:    "worker-0",
					IP:      "10.0.0.11",
					Role:    v1.StaticNodeRoleWorker,
					SSHAuth: &v1.Auth{SSHUser: "ray", SSHPrivateKey: "/tmp/key"},
				},
				{
					Name:    "head-0",
					IP:      "10.0.0.10",
					Role:    v1.StaticNodeRoleHead,
					SSHAuth: &v1.Auth{SSHUser: "ray", SSHPrivateKey: "/tmp/key"},
				},
			},
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
		Type: v1.AcceleratorTypeNVIDIAGPU.String(),
	}
}

func cpuAcceleratorStatus() v1.StaticNodeAcceleratorStatus {
	return v1.CPUStaticNodeAcceleratorStatus()
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

type fakeAcceleratorProfileProvider struct {
	profiles map[string]*v1.AcceleratorProfile
}

func (f fakeAcceleratorProfileProvider) GetAcceleratorProfile(
	_ context.Context,
	acceleratorType string,
) (*v1.AcceleratorProfile, error) {
	profile, ok := f.profiles[acceleratorType]
	if !ok {
		return nil, fmt.Errorf("accelerator profile %s not found", acceleratorType)
	}

	return profile, nil
}

func assertNodeComponentNames(t *testing.T, components []v1.NodeComponentSpec, want []string) {
	t.Helper()

	require.Len(t, components, len(want))
	for i, component := range components {
		assert.Equal(t, want[i], component.Name)
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

func assertNotContainsVolume(t *testing.T, volumes []v1.NodeComponentVolume, name string) {
	t.Helper()

	for _, volume := range volumes {
		assert.NotEqual(t, name, volume.Name)
	}
}

func requireVolume(
	t *testing.T,
	component *v1.NodeComponentSpec,
	name string,
	hostPath string,
	mountPath string,
) v1.NodeComponentVolume {
	t.Helper()

	require.NotNil(t, component)
	for _, volume := range component.Volumes {
		if volume.Name != name {
			continue
		}

		assert.Equal(t, hostPath, volume.HostPath)
		assert.Equal(t, mountPath, volume.MountPath)
		assert.True(t, volume.ReadOnly)

		return volume
	}

	t.Fatalf("expected component %s to have volume %s", component.Name, name)

	return v1.NodeComponentVolume{}
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

func plannedStaticNodes(
	t *testing.T,
	planner *Planner,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) []*v1.StaticNode {
	t.Helper()

	desiredNodePlans, err := planner.Plan(context.Background(), cluster, currentNodes)
	require.NoError(t, err)

	return staticNodesFromPlans(desiredNodePlans)
}

func staticNodesFromPlans(plans []DesiredNodePlan) []*v1.StaticNode {
	nodes := make([]*v1.StaticNode, 0, len(plans))
	for _, plan := range plans {
		nodes = append(nodes, plan.Node)
	}

	return nodes
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
		Image: oldRayImage,
	}
	workerRay := v1.NodeComponentSpec{
		Name:  "ray-worker",
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
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU},
				Warm:        &v1.WarmStatus{Ready: true},
				Components: []v1.NodeComponentStatus{
					{
						Name:          "ray-head",
						Ready:         true,
						Phase:         v1.NodeComponentPhaseRunning,
						ObservedImage: oldRayImage,
					},
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
				Accelerator: &v1.StaticNodeAcceleratorStatus{Type: v1.StaticNodeAcceleratorTypeCPU},
				Warm:        &v1.WarmStatus{Ready: true},
				Components: []v1.NodeComponentStatus{
					{
						Name:          "ray-worker",
						Ready:         true,
						Phase:         v1.NodeComponentPhaseRunning,
						ObservedImage: oldRayImage,
					},
				},
			},
		},
	}
}

func markUpgradeWorkersStopped(nodes []*v1.StaticNode) {
	worker := findStaticNode(nodes, "worker-0")
	if worker == nil || worker.Status == nil {
		return
	}

	worker.Status.Components = []v1.NodeComponentStatus{
		{Name: "ray-worker", Phase: v1.NodeComponentPhaseStopped},
	}
}

func markUpgradeHeadTargetRunning(nodes []*v1.StaticNode) {
	markUpgradeWorkersStopped(nodes)

	head := findStaticNode(nodes, "head-0")
	if head == nil || head.Status == nil {
		return
	}

	head.Status.Components = []v1.NodeComponentStatus{
		{
			Name:          "ray-head",
			Ready:         true,
			Phase:         v1.NodeComponentPhaseRunning,
			ObservedImage: "registry.example.com/neutree/neutree/neutree-serve:v1.2.1",
		},
	}
}

func markStaticNodeUpgradeReady(
	t *testing.T,
	planner *Planner,
	cluster *v1.StaticNodeCluster,
	nodes []*v1.StaticNode,
	rayImage string,
) {
	t.Helper()
	if planner == nil {
		planner = &Planner{}
	}

	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Status == nil {
			continue
		}

		componentName := "ray-worker"
		if node.Metadata.Name == "head-0" {
			componentName = "ray-head"
		}

		node.Status.Phase = v1.StaticNodePhaseReady
		node.Status.Warm = &v1.WarmStatus{Ready: true}
		node.Status.Components = []v1.NodeComponentStatus{
			{
				Name:          componentName,
				Ready:         true,
				Phase:         v1.NodeComponentPhaseRunning,
				ObservedImage: rayImage,
			},
		}
	}

	desiredNodes := plannedStaticNodes(t, planner, cluster, nodes)

	for _, desired := range desiredNodes {
		current := findStaticNode(nodes, desired.Metadata.Name)
		require.NotNil(t, current)
		require.NotNil(t, current.Status)

		current.Spec = desired.Spec
		current.Status.Components = make([]v1.NodeComponentStatus, 0, len(desired.Spec.Components))
		for _, component := range desired.Spec.Components {
			current.Status.Components = append(current.Status.Components, v1.NodeComponentStatus{
				Name:          component.Name,
				Ready:         true,
				Phase:         v1.NodeComponentPhaseRunning,
				ObservedHash:  component.ConfigHash,
				ObservedImage: component.Image,
			})
		}
	}
}
