package bench

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/ray/rayserve"
)

var dockerContainerIDPattern = regexp.MustCompile(`(?i)docker(?:[-/])([0-9a-f]{12,64})(?:\.scope)?`)

func resolveStaticTarget(ctx context.Context, cluster, credentialCluster *v1.Cluster, endpoint *v1.Endpoint) (BenchTarget, error) {
	if err := ctx.Err(); err != nil {
		return BenchTarget{}, err
	}

	if cluster == nil || cluster.Status == nil || strings.TrimSpace(cluster.Status.DashboardURL) == "" {
		return BenchTarget{}, fmt.Errorf("cluster dashboard URL is required for SSH benchmark")
	}

	appName, err := endpointApplicationName(endpoint)
	if err != nil {
		return BenchTarget{}, err
	}

	service := dashboard.NewDashboardService(cluster.Status.DashboardURL)
	if client, ok := service.(*dashboard.Client); ok {
		service = client.WithContext(ctx)
	}

	applications, err := service.GetServeApplications()

	if err != nil {
		return BenchTarget{}, fmt.Errorf("get Ray Serve applications: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return BenchTarget{}, err
	}

	application, ok := applications.Applications[appName]
	if !ok {
		return BenchTarget{}, fmt.Errorf("Ray Serve application %q not found", appName)
	}

	replica, err := selectBackendReplica(application.Deployments)
	if err != nil {
		return BenchTarget{}, err
	}

	actor, err := rayserve.ActorByID(service, replica.ActorID)
	if err != nil {
		return BenchTarget{}, fmt.Errorf("resolve Backend actor %s: %w", replica.ActorID, err)
	}

	if err := ctx.Err(); err != nil {
		return BenchTarget{}, err
	}

	if actor == nil || actor.PID <= 0 {
		return BenchTarget{}, fmt.Errorf("Backend actor %s has no running PID", replica.ActorID)
	}

	nodes, err := service.ListNodes()
	if err != nil {
		return BenchTarget{}, fmt.Errorf("list Ray nodes: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return BenchTarget{}, err
	}

	nodeIP, err := nodeIPForRayNode(nodes, backendNodeID(actor, replica))
	if err != nil {
		return BenchTarget{}, err
	}

	auth, err := sshAuthForCluster(credentialCluster)
	if err != nil {
		return BenchTarget{}, err
	}

	sshRunner, err := newSSHRemoteRunner(auth, nodeIP)
	if err != nil {
		return BenchTarget{}, err
	}

	cgroup, err := runRemoteOutput(ctx, sshRunner, "cat -- /proc/"+strconv.Itoa(actor.PID)+"/cgroup")
	if err != nil {
		_ = sshRunner.Close()
		return BenchTarget{}, fmt.Errorf("read cgroup for Backend actor %s: %w", replica.ActorID, err)
	}

	containerID, err := dockerContainerIDFromCgroup(cgroup)
	if err != nil {
		_ = sshRunner.Close()
		return BenchTarget{}, fmt.Errorf("resolve Backend container for actor %s: %w", replica.ActorID, err)
	}

	fullContainerID, err := runRemoteOutput(ctx, sshRunner,
		"docker inspect --format '{{.Id}}' "+shellQuote(containerID))
	if err != nil {
		_ = sshRunner.Close()
		return BenchTarget{}, fmt.Errorf("verify Docker container for actor %s: %w", replica.ActorID, err)
	}

	fullContainerID = strings.TrimSpace(fullContainerID)
	if !validDockerContainerID(fullContainerID) {
		_ = sshRunner.Close()
		return BenchTarget{}, fmt.Errorf("Docker inspect returned an invalid container ID")
	}

	tokenizer, err := tokenizerOptionsFromRayApplication(application)
	if err != nil {
		_ = sshRunner.Close()
		return BenchTarget{}, err
	}

	baseURL, err := staticServeBaseURL(cluster, endpoint)
	if err != nil {
		_ = sshRunner.Close()
		return BenchTarget{}, err
	}

	return BenchTarget{
		Platform:  "ssh",
		RuntimeID: "docker/" + fullContainerID + "@" + nodeIP,
		BaseURL:   baseURL,
		Tokenizer: tokenizer,
		runner: dockerRemoteRunner{
			ssh:         sshRunner,
			containerID: fullContainerID,
		},
		cleanup: sshRunner.Close,
	}, nil
}

func selectBackendReplica(deployments map[string]dashboard.Deployment) (dashboard.Replica, error) {
	candidates := make([]dashboard.Replica, 0)

	for key, deployment := range deployments {
		name := deployment.Name
		if name == "" {
			name = key
		}

		if !strings.EqualFold(name, "backend") {
			continue
		}

		for _, replica := range deployment.Replicas {
			if replica.ActorID != "" {
				candidates = append(candidates, replica)
			}
		}
	}

	if len(candidates) == 0 {
		return dashboard.Replica{}, fmt.Errorf("no Backend replica with an actor ID found")
	}

	candidates = preferRunningReplicas(candidates)
	if len(candidates) == 0 {
		return dashboard.Replica{}, fmt.Errorf("no RUNNING Backend replica found")
	}

	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].NodeID != candidates[right].NodeID {
			return candidates[left].NodeID < candidates[right].NodeID
		}

		if candidates[left].ReplicaID != candidates[right].ReplicaID {
			return candidates[left].ReplicaID < candidates[right].ReplicaID
		}

		return candidates[left].ActorID < candidates[right].ActorID
	})

	return candidates[0], nil
}

// preferRunningReplicas keeps only RUNNING replicas when Ray reports replica
// states. Older Ray versions omit the state, so an unknown state keeps the
// previous selection behaviour instead of rejecting every replica.
func preferRunningReplicas(candidates []dashboard.Replica) []dashboard.Replica {
	statesKnown := false

	for _, replica := range candidates {
		if replica.State != "" {
			statesKnown = true
			break
		}
	}

	if !statesKnown {
		return candidates
	}

	running := make([]dashboard.Replica, 0, len(candidates))

	for _, replica := range candidates {
		if strings.EqualFold(replica.State, dashboard.ApplicationStatusRunning) {
			running = append(running, replica)
		}
	}

	if len(running) == 0 {
		return nil
	}

	return running
}

func dockerContainerIDFromCgroup(cgroup string) (string, error) {
	matches := dockerContainerIDPattern.FindAllStringSubmatch(cgroup, -1)
	ids := make(map[string]struct{}, len(matches))

	for _, match := range matches {
		if len(match) > 1 {
			ids[strings.ToLower(match[1])] = struct{}{}
		}
	}

	if len(ids) == 0 {
		return "", fmt.Errorf("no Docker container ID found in cgroup")
	}

	if len(ids) > 1 {
		return "", fmt.Errorf("ambiguous Docker container IDs found in cgroup")
	}

	for id := range ids {
		return id, nil
	}

	return "", fmt.Errorf("no Docker container ID found in cgroup")
}

func validDockerContainerID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}

	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') &&
			!(character >= 'A' && character <= 'F') {
			return false
		}
	}

	return true
}

func endpointApplicationName(endpoint *v1.Endpoint) (string, error) {
	if endpoint == nil || endpoint.Metadata == nil {
		return "", fmt.Errorf("Endpoint metadata with workspace and name is required")
	}

	if endpoint.Metadata.Workspace == "" || endpoint.Metadata.Name == "" {
		return "", fmt.Errorf("Endpoint metadata with workspace and name is required")
	}

	return endpoint.Metadata.Workspace + "_" + endpoint.Metadata.Name, nil
}

func nodeIPForRayNode(nodes []v1.NodeSummary, nodeID string) (string, error) {
	for _, node := range nodes {
		if node.Raylet.NodeID == nodeID && node.Raylet.State == v1.AliveNodeState && node.IP != "" {
			return node.IP, nil
		}
	}

	return "", fmt.Errorf("Ray node %q is not alive or has no IP", nodeID)
}

func backendNodeID(actor *dashboard.Actor, replica dashboard.Replica) string {
	if actor != nil && actor.NodeID != "" {
		return actor.NodeID
	}

	return replica.NodeID
}

func sshAuthForCluster(cluster *v1.Cluster) (v1.Auth, error) {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Config == nil || cluster.Spec.Config.SSHConfig == nil {
		return v1.Auth{}, fmt.Errorf("SSH cluster credentials are unavailable")
	}

	return cluster.Spec.Config.SSHConfig.Auth, nil
}

// tokenizerOptionsFromRayApplication reads the tokenizer path and the vLLM
// tokenizer engine arguments the Ray Serve application was deployed with.
func tokenizerOptionsFromRayApplication(application dashboard.RayServeApplicationStatus) (tokenizerOptions, error) {
	if application.DeployedAppConfig == nil || application.DeployedAppConfig.Args == nil {
		return tokenizerOptions{}, fmt.Errorf("Ray Serve model arguments are unavailable")
	}

	model, ok := application.DeployedAppConfig.Args["model"].(map[string]interface{})
	if !ok {
		return tokenizerOptions{}, fmt.Errorf("Ray Serve model arguments are invalid")
	}

	path, ok := model["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return tokenizerOptions{}, fmt.Errorf("Ray Serve tokenizer path is unavailable")
	}

	options := tokenizerOptions{Path: path}

	if override, ok := application.DeployedAppConfig.Args["tokenizer"].(string); ok && strings.TrimSpace(override) != "" {
		options.Path = override
	}

	if mode, ok := application.DeployedAppConfig.Args["tokenizer_mode"].(string); ok {
		options.Mode = mode
	}

	if trust, ok := boolVariable(application.DeployedAppConfig.Args["trust_remote_code"]); ok {
		options.TrustRemoteCode = trust
	}

	return options, nil
}

// boolVariable reads a boolean engine argument, which Endpoint variables may
// carry either as a JSON boolean or as the string the Ray path does not coerce
// before the benchmark reads the deployed config.
func boolVariable(value interface{}) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true"), true
	default:
		return false, false
	}
}

func staticServeBaseURL(cluster *v1.Cluster, endpoint *v1.Endpoint) (string, error) {
	if cluster == nil || cluster.Status == nil || strings.TrimSpace(cluster.Status.DashboardURL) == "" {
		return "", fmt.Errorf("cluster dashboard URL is required")
	}

	if endpoint == nil || endpoint.Metadata == nil || endpoint.Metadata.Workspace == "" || endpoint.Metadata.Name == "" {
		return "", fmt.Errorf("Endpoint metadata with workspace and name is required")
	}

	dashboardURL, err := url.Parse(cluster.Status.DashboardURL)
	if err != nil {
		return "", fmt.Errorf("parse cluster dashboard URL: %w", err)
	}

	if dashboardURL.Scheme == "" || dashboardURL.Hostname() == "" {
		return "", fmt.Errorf("cluster dashboard URL must include scheme and host")
	}

	return (&url.URL{
		Scheme: dashboardURL.Scheme,
		Host:   net.JoinHostPort(dashboardURL.Hostname(), "8000"),
		Path:   "/" + endpoint.Metadata.Workspace + "/" + endpoint.Metadata.Name,
	}).String(), nil
}
