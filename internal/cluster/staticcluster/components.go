package staticcluster

import (
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/staticcomponent"
)

const (
	rayHeadComponentName    = "ray-head"
	rayWorkerComponentName  = "ray-worker"
	rayRuntimeWarmImageName = "ray-runtime"
	rayDockerConfigHostDir  = "/etc/neutree/docker"
	rayDockerConfigMountDir = "/root/.docker"
)

func buildNodeComponents(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	profileComponents v1.ClusterProfileComponents,
	profile *v1.AcceleratorProfile,
	metricsRemoteWriteURL string,
) ([]v1.NodeComponentSpec, error) {
	role := v1.StaticNodeRoleWorker
	if node != nil && node.Spec != nil {
		role = node.Spec.Role
	}

	rayComponent, err := buildRayComponent(cluster, role, profileComponents.RayRuntime, profile)
	if err != nil {
		return nil, err
	}

	metricsComponents, err := buildMetricsComponents(cluster, node, role, profileComponents, profile, metricsRemoteWriteURL)
	if err != nil {
		return nil, err
	}

	return append([]v1.NodeComponentSpec{rayComponent}, metricsComponents...), nil
}

func withComponentConfigHashes(components []v1.NodeComponentSpec) []v1.NodeComponentSpec {
	return staticcomponent.WithHashes(components)
}

func buildRayComponent(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
	runtime v1.ImageRef,
	profile *v1.AcceleratorProfile,
) (v1.NodeComponentSpec, error) {
	image, err := buildRayRuntimeImage(cluster, runtime, profile)
	if err != nil {
		return v1.NodeComponentSpec{}, err
	}

	env := rayRuntimeEnv(profile)
	dockerRunOptions := rayRuntimeDockerRunOptions(profile)
	command := []string{"/bin/bash", "-lc"}

	if role == v1.StaticNodeRoleHead {
		return v1.NodeComponentSpec{
			Name:             rayHeadComponentName,
			Image:            image,
			Command:          command,
			Args:             []string{rayStartCommand(cluster, role)},
			Env:              env,
			DockerRunOptions: dockerRunOptions,
			HealthCheck: &v1.NodeComponentHealthCheck{
				Port: v1.RayletMetricsPort,
			},
		}, nil
	}

	return v1.NodeComponentSpec{
		Name:             rayWorkerComponentName,
		Image:            image,
		Command:          command,
		Args:             []string{rayStartCommand(cluster, role)},
		Env:              env,
		DockerRunOptions: dockerRunOptions,
		HealthCheck: &v1.NodeComponentHealthCheck{
			Port: v1.RayletMetricsPort,
		},
	}, nil
}

func rayRuntimeEnv(profile *v1.AcceleratorProfile) map[string]string {
	env := map[string]string{
		"RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION": "0.1",
		"DOCKER_CONFIG":                                  rayDockerConfigMountDir,
		"RAY_enable_open_telemetry":                      "false",
		"RAY_EXPERIMENTAL_RUNTIME_ENV_CONTAINER_RUNTIME": "docker",
		"RAY_process_group_cleanup_enabled":              "true",
	}

	if profile == nil || profile.ClusterRuntime == nil {
		return env
	}

	for key, value := range profile.ClusterRuntime.Env {
		env[key] = value
	}

	return env
}

func rayRuntimeDockerRunOptions(profile *v1.AcceleratorProfile) []string {
	options := []string{
		"--net=host",
		"--ulimit nofile=65536:65536",
		"--volume /var/run/docker.sock:/var/run/docker.sock",
		"--volume " + rayDockerConfigHostDir + ":" + rayDockerConfigMountDir + ":ro",
		"--volume /tmp:/tmp",
		"--pid=host",
		"--ipc=host",
	}

	if profile == nil || profile.ClusterRuntime == nil {
		return options
	}

	if profile.ClusterRuntime.Runtime != "" {
		options = append(options, "--runtime="+profile.ClusterRuntime.Runtime)
	}

	options = append(options, profile.ClusterRuntime.Options...)

	return options
}

func rayStartCommand(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
) string {
	parts := []string{
		"ray stop",
		"ulimit -n 65536",
	}

	commonArgs := strings.Join([]string{
		"--disable-usage-stats",
		"--node-manager-port=8077",
		"--dashboard-agent-listen-port=52365",
		"--min-worker-port=10002",
		"--max-worker-port=20000",
		"--runtime-env-agent-port=56999",
		fmt.Sprintf("--metrics-export-port=%d", v1.RayletMetricsPort),
	}, " ")

	if role == v1.StaticNodeRoleHead {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --head --port=6379 --dashboard-host=0.0.0.0",
			commonArgs,
			fmt.Sprintf("--dashboard-port=%d", v1.RayDashboardPort),
			"--ray-client-server-port=10001",
			"--block",
			rayNodeLabelArg(cluster, role),
		}, " "))
	} else {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --address=" + staticNodeClusterHeadIP(cluster) + ":6379",
			commonArgs,
			"--block",
			rayNodeLabelArg(cluster, role),
		}, " "))
	}

	return strings.Join(parts, " && ")
}

func rayNodeLabelArg(cluster *v1.StaticNodeCluster, role v1.StaticNodeRole) string {
	labels := rayNodeLabels(cluster, role)

	content, err := json.Marshal(labels)
	if err != nil || len(labels) == 0 {
		return ""
	}

	return "--labels='" + string(content) + "'"
}

func rayNodeLabels(cluster *v1.StaticNodeCluster, role v1.StaticNodeRole) map[string]string {
	labels := map[string]string{}
	if cluster != nil && cluster.Spec != nil && cluster.Spec.Version != "" {
		labels[v1.NeutreeServingVersionLabel] = cluster.Spec.Version
	}

	if role == v1.StaticNodeRoleWorker {
		labels[v1.NeutreeNodeProvisionTypeLabel] = v1.StaticNodeProvisionType
	}

	return labels
}

func staticNodeClusterHeadIP(cluster *v1.StaticNodeCluster) string {
	if cluster == nil || cluster.Spec == nil {
		return ""
	}

	for _, node := range cluster.Spec.Nodes {
		if normalizeStaticNodeRole(node.Role) == v1.StaticNodeRoleHead {
			return node.IP
		}
	}

	return ""
}

func buildNodeWarmSpec(components []v1.NodeComponentSpec) *v1.WarmSpec {
	if len(components) == 0 {
		return nil
	}

	images := make([]v1.WarmImageSpec, 0, len(components))
	seen := map[string]struct{}{}

	for _, component := range components {
		if component.Image == "" {
			continue
		}

		if _, exists := seen[component.Image]; exists {
			continue
		}

		seen[component.Image] = struct{}{}

		images = append(images, v1.WarmImageSpec{
			Name:     warmImageName(component),
			Ref:      component.Image,
			Required: true,
		})
	}

	if len(images) == 0 {
		return nil
	}

	return &v1.WarmSpec{
		Images: images,
	}
}

func buildRayRuntimeImage(
	cluster *v1.StaticNodeCluster,
	component v1.ImageRef,
	acceleratorProfile *v1.AcceleratorProfile,
) (string, error) {
	if acceleratorProfile != nil && acceleratorProfile.ClusterRuntime != nil {
		if suffix := strings.TrimSpace(acceleratorProfile.ClusterRuntime.ImageSuffix); suffix != "" {
			component.Tag += "-" + suffix
		}
	}

	return staticProfileComponentImage(cluster, "ray runtime", component)
}

func warmImageName(component v1.NodeComponentSpec) string {
	if isRayComponentName(component.Name) {
		return rayRuntimeWarmImageName
	}

	return component.Name
}
