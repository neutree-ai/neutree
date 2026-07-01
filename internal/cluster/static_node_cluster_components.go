package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func buildNodeComponents(
	cluster *v1.StaticNodeCluster,
	node *v1.StaticNode,
	profile *v1.AcceleratorProfile,
) []v1.NodeComponentSpec {
	role := v1.StaticNodeRoleWorker
	if node != nil && node.Spec != nil {
		role = node.Spec.Role
	}

	return []v1.NodeComponentSpec{buildRayComponent(cluster, role, profile)}
}

func withComponentConfigHashes(components []v1.NodeComponentSpec) []v1.NodeComponentSpec {
	result := make([]v1.NodeComponentSpec, len(components))
	for i, component := range components {
		result[i] = component
		result[i].ConfigHash = nodeComponentConfigHash(component)
	}

	return result
}

func nodeComponentConfigHash(component v1.NodeComponentSpec) string {
	component.ConfigHash = ""
	component.ConfigFiles = append([]v1.NodeComponentConfigFile{}, component.ConfigFiles...)

	for i := range component.ConfigFiles {
		if component.ConfigFiles[i].SkipRestartOnChange {
			component.ConfigFiles[i].Content = ""
		}
	}

	content, err := json.Marshal(component)
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(content)

	return hex.EncodeToString(sum[:])
}

func buildRayComponent(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
	profile *v1.AcceleratorProfile,
) v1.NodeComponentSpec {
	image := buildRayRuntimeImage(cluster, clusterRuntimeImageSuffix(profile))
	env := rayRuntimeEnv(profile)
	dockerRunOptions := rayRuntimeDockerRunOptions(profile)
	command := []string{"/bin/bash", "-lc"}

	if role == v1.StaticNodeRoleHead {
		return v1.NodeComponentSpec{
			Name:             "ray-head",
			Type:             v1.NodeComponentTypeRayHead,
			Image:            image,
			Command:          command,
			Args:             []string{rayStartCommand(cluster, role)},
			Env:              env,
			DockerRunOptions: dockerRunOptions,
			HealthCheck: &v1.NodeComponentHealthCheck{
				Port:          defaultRayDashboardPort,
				RayNodeLabels: rayNodeLabels(cluster, role),
			},
		}
	}

	return v1.NodeComponentSpec{
		Name:             "ray-worker",
		Type:             v1.NodeComponentTypeRayWorker,
		Image:            image,
		Command:          command,
		Args:             []string{rayStartCommand(cluster, role)},
		Env:              env,
		DockerRunOptions: dockerRunOptions,
		HealthCheck: &v1.NodeComponentHealthCheck{
			HTTPHost:      staticNodeClusterHeadIP(cluster),
			Port:          defaultRayDashboardPort,
			RayNodeLabels: rayNodeLabels(cluster, role),
		},
	}
}

func rayRuntimeEnv(profile *v1.AcceleratorProfile) map[string]string {
	env := map[string]string{
		"RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION":     "0.1",
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

func clusterRuntimeImageSuffix(profile *v1.AcceleratorProfile) string {
	if profile == nil || profile.ClusterRuntime == nil {
		return ""
	}

	return profile.ClusterRuntime.ImageSuffix
}

func rayStartCommand(
	cluster *v1.StaticNodeCluster,
	role v1.StaticNodeRole,
) string {
	parts := []string{
		legacyRayContainerCleanupWatcherCommand(),
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
			"--dashboard-port=8265",
			"--ray-client-server-port=10001",
			rayNodeLabelArg(cluster, role),
		}, " "))
	} else {
		parts = append(parts, strings.Join([]string{
			"python /home/ray/start.py --address=" + staticNodeClusterHeadIP(cluster) + ":6379",
			commonArgs,
			rayNodeLabelArg(cluster, role),
		}, " "))
	}

	parts = append(parts, "tail -f /dev/null")

	return strings.Join(parts, " && ")
}

func legacyRayContainerCleanupCommand() string {
	return "docker rm -f ray_container >/dev/null 2>&1 || true"
}

func legacyRayContainerCleanupWatcherCommand() string {
	return "(while true; do " + legacyRayContainerCleanupCommand() + "; sleep 1; done) & true"
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

func buildRayRuntimeImage(cluster *v1.StaticNodeCluster, imageSuffixes ...string) string {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Version == "" || cluster.Spec.ImageRegistry == "" {
		return ""
	}

	imageSuffix := ""
	if len(imageSuffixes) > 0 {
		imageSuffix = imageSuffixes[0]
	}

	return util.BuildClusterImageRef(strings.TrimRight(cluster.Spec.ImageRegistry, "/"), cluster.Spec.Version, imageSuffix)
}

func warmImageName(component v1.NodeComponentSpec) string {
	switch component.Type {
	case v1.NodeComponentTypeRayHead, v1.NodeComponentTypeRayWorker:
		return "ray-runtime"
	default:
		if component.Name != "" {
			return component.Name
		}

		return string(component.Type)
	}
}
