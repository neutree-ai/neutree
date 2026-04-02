package cluster

import (
	"fmt"
	"path"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/semver"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func (c *sshRayClusterReconciler) upCluster(reconcileCtx *ReconcileContext, restart bool) (string, error) {
	dockerConfig, changed, err := c.buildAcceleratorDockerConfig(reconcileCtx, reconcileCtx.sshClusterConfig.Provider.HeadIP)
	if err != nil {
		return "", errors.Wrap(err, "failed to build accelerator docker config")
	}

	if changed {
		// Build a temporary config copy with accelerator-specific Docker settings for ray up.
		upConfig := *reconcileCtx.sshRayClusterConfig
		upConfig.Docker = dockerConfig

		err = reconcileCtx.sshConfigGenerator.Cleanup()
		if err != nil {
			return "", errors.Wrap(err, "failed to cleanup config")
		}

		err = reconcileCtx.sshConfigGenerator.Generate(&upConfig)
		if err != nil {
			return "", errors.Wrap(err, "failed to generate config")
		}
	}

	// Set RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION=0.1 to keep head node shm size consistent with worker nodes
	upArgs := []string{
		fmt.Sprintf("RAY_TMPDIR=%s", reconcileCtx.sshConfigGenerator.BasePath()),
		"RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION=0.1",
		"ray", "up", "--disable-usage-stats", "--no-config-cache", "-y", "-v",
	}
	if !restart {
		upArgs = append(upArgs, "--no-restart")
	}

	upArgs = append(upArgs, reconcileCtx.sshConfigGenerator.ConfigPath())

	klog.V(4).Infof("Up args: %s", strings.Join(upArgs, " "))

	output, err := c.executor.Execute(reconcileCtx.Ctx, "bash", []string{"-c", strings.Join(upArgs, " ")})
	if err != nil {
		return "", errors.Wrap(err, "failed to run ray up: "+string(output))
	}

	klog.V(4).Infof("Ray cluster up output: %s", string(output))

	return reconcileCtx.sshClusterConfig.Provider.HeadIP, nil
}

func (c *sshRayClusterReconciler) startNode(reconcileCtx *ReconcileContext, nodeIP string) error {
	dockerConfig, _, err := c.buildAcceleratorDockerConfig(reconcileCtx, nodeIP)
	if err != nil {
		return errors.Wrap(err, "failed to build accelerator docker config")
	}

	sshCommandArgs := c.buildSSHCommandArgs(reconcileCtx, nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&dockerConfig, sshCommandArgs)

	env := map[string]interface{}{
		"RAY_HEAD_IP": reconcileCtx.sshRayClusterConfig.Provider.HeadIP,
	}

	for _, command := range reconcileCtx.sshRayClusterConfig.InitializationCommands {
		_, err = dockerCommandRunner.Run(reconcileCtx.Ctx, command, true, nil, false, env, "host", "", false)
		if err != nil {
			return errors.Wrap(err, "failed to run command "+command)
		}
	}

	succeed, err := dockerCommandRunner.RunInit(reconcileCtx.Ctx)
	if err != nil {
		return errors.Wrap(err, "failed to run docker runtime init")
	}

	if !succeed {
		return errors.New("failed to run docker runtime init")
	}

	for _, command := range reconcileCtx.sshRayClusterConfig.StaticWorkerStartRayCommands {
		_, err = dockerCommandRunner.Run(reconcileCtx.Ctx, command, true, nil, false, env, "docker", "", false)
		if err != nil {
			return errors.Wrap(err, "failed to run command "+command)
		}
	}

	return nil
}

func (c *sshRayClusterReconciler) drainNode(reconcileCtx *ReconcileContext, nodeID, reason, message string, deadlineRemainSeconds int) error {
	gcsServerURL := reconcileCtx.sshRayClusterConfig.Provider.HeadIP + ":6379"
	drainArgs := []string{
		fmt.Sprintf("RAY_TMPDIR=%s", reconcileCtx.sshConfigGenerator.BasePath()),
		"ray",
		"drain-node",
		"--address=" + gcsServerURL,
		"--node-id=" + nodeID,
		"--reason=" + reason,
		fmt.Sprintf(`--reason-message="%s"`, message),
		"--deadline-remaining-seconds=" + fmt.Sprintf("%d", deadlineRemainSeconds),
	}

	output, err := c.executor.Execute(reconcileCtx.Ctx, "bash", []string{"-c", strings.Join(drainArgs, " ")})
	if err != nil {
		return errors.Wrap(err, "failed to run ray drain node: "+string(output))
	}

	klog.V(4).Infof("Ray drain-node output: %s", string(output))

	return nil
}

func (c *sshRayClusterReconciler) getNodeByIP(reconcileCtx *ReconcileContext, nodeIP string) (*v1.NodeSummary, error) {
	rayNodes, err := reconcileCtx.rayService.ListNodes()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list ray nodes")
	}

	for i := range rayNodes {
		if rayNodes[i].IP == nodeIP {
			return &rayNodes[i], nil
		}
	}

	return nil, ErrorRayNodeNotFound
}

// stopNode will stop ray process and docker container on the node.
// if force is false, it will drain the node before stopping the ray process.
// if force is true, it will directly stop the ray process.
func (c *sshRayClusterReconciler) stopNode(reconcileCtx *ReconcileContext, nodeIP string, force bool) error {
	sshCommandArgs := c.buildSSHCommandArgs(reconcileCtx, nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&reconcileCtx.sshRayClusterConfig.Docker, sshCommandArgs)

	dockerInstalled, err := dockerCommandRunner.CheckDockerInstalled(reconcileCtx.Ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check docker installed")
	}

	if !dockerInstalled {
		return nil
	}

	containerRunning, err := dockerCommandRunner.CheckContainerStatus(reconcileCtx.Ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check container status")
	}

	if !containerRunning {
		return nil
	}

	if !force {
		node, err := c.getNodeByIP(reconcileCtx, nodeIP)
		if err != nil {
			// no need to stop node if node not found.
			if err == ErrorRayNodeNotFound {
				return nil
			} else {
				return errors.Wrap(err, "failed to get node ID")
			}
		}

		if node.Raylet.State == v1.AliveNodeState {
			// current drainNode behavior is similar to ray stop, and the ray community will optimize it later.
			err = c.drainNode(reconcileCtx, node.Raylet.NodeID, "DRAIN_NODE_REASON_PREEMPTION", "stop node", 600)
			if err != nil {
				return errors.Wrap(err, "failed to drain node "+nodeIP)
			}
		}
	}

	_, err = dockerCommandRunner.Run(reconcileCtx.Ctx, "ray stop", true, nil, false, nil, "docker", "", false)
	if err != nil {
		return errors.Wrap(err, "failed to stop ray process")
	}

	_, err = dockerCommandRunner.Run(reconcileCtx.Ctx, "docker stop "+reconcileCtx.sshRayClusterConfig.Docker.ContainerName, true, nil, false, nil, "host", "", false)
	if err != nil {
		return errors.Wrap(err, "failed to stop docker container")
	}

	return nil
}

func (c *sshRayClusterReconciler) getDashboardService(headIP string) dashboard.DashboardService {
	return dashboard.NewDashboardService(fmt.Sprintf("http://%s:8265", headIP))
}

func (c *sshRayClusterReconciler) downCluster(reconcileCtx *ReconcileContext) error {
	klog.Infof("Downing ray ssh cluster %s", reconcileCtx.Cluster.Metadata.WorkspaceName())
	// first stop all static node
	eg := &errgroup.Group{}

	for i := range reconcileCtx.sshClusterConfig.Provider.WorkerIPs {
		nodeIP := reconcileCtx.sshClusterConfig.Provider.WorkerIPs[i]

		eg.Go(func() error {
			return c.stopNode(reconcileCtx, nodeIP, true)
		})
	}

	// best effort to stop node, ignore error.
	eg.Wait() //nolint:errcheck

	downArgs := []string{
		fmt.Sprintf("RAY_TMPDIR=%s", reconcileCtx.sshConfigGenerator.BasePath()),
		"ray",
		"down",
		"-y",
		"-v",
	}

	downArgs = append(downArgs, reconcileCtx.sshConfigGenerator.ConfigPath())

	klog.V(4).Infof("Down args: %s", strings.Join(downArgs, " "))

	output, err := c.executor.Execute(reconcileCtx.Ctx, "bash", []string{"-c", strings.Join(downArgs, " ")})
	if err != nil {
		return errors.Wrap(err, "failed to run ray down cluster: "+string(output))
	}

	klog.V(4).Infof("Ray cluster down output: %s", string(output))
	klog.Infof("Ray ssh cluster %s downed successfully", reconcileCtx.Cluster.Metadata.WorkspaceName())

	return nil
}

// buildAcceleratorDockerConfig returns a copy of the base Docker config with
// accelerator runtime options (--runtime, -e, custom options) appended for the
// given node. The original reconcileCtx.sshRayClusterConfig is never modified,
// so this function is safe to call multiple times for different nodes.
func (c *sshRayClusterReconciler) buildAcceleratorDockerConfig(reconcileCtx *ReconcileContext, nodeIP string) (v1.Docker, bool, error) {
	if reconcileCtx.Cluster.Status == nil || reconcileCtx.Cluster.Status.AcceleratorType == nil {
		return v1.Docker{}, false, errors.New("cluster status or accelerator type is not set")
	}

	base := reconcileCtx.sshRayClusterConfig.Docker

	// Deep copy RunOptions slice to avoid mutating the original.
	runOptions := make([]string, len(base.RunOptions))
	copy(runOptions, base.RunOptions)

	runtimeConfig, err := c.acceleratorManager.GetNodeRuntimeConfig(reconcileCtx.Ctx,
		*reconcileCtx.Cluster.Status.AcceleratorType, nodeIP, reconcileCtx.sshClusterConfig.Auth)
	if err != nil {
		return v1.Docker{}, false, errors.Wrap(err, "failed to get node runtime config")
	}

	changed := false
	image := base.Image

	if runtimeConfig.ImageSuffix != "" {
		changed = true
		image = image + "-" + runtimeConfig.ImageSuffix
	}

	if runtimeConfig.Runtime != "" {
		changed = true

		runOptions = append(runOptions, "--runtime="+runtimeConfig.Runtime)
	}

	if runtimeConfig.Env != nil {
		changed = true

		for k, v := range runtimeConfig.Env {
			runOptions = append(runOptions, fmt.Sprintf("-e %s=%s", k, v))
		}
	}

	if runtimeConfig.Options != nil {
		changed = true

		runOptions = append(runOptions, runtimeConfig.Options...)
	}

	dockerConfig := base
	dockerConfig.Image = image
	dockerConfig.RunOptions = runOptions

	return dockerConfig, changed, nil
}

// setDefaultRayClusterConfig set default ray cluster config.
func (c *sshRayClusterReconciler) generateRayClusterConfig(reconcileContext *ReconcileContext) (*v1.RayClusterConfig, error) {
	rayClusterConfig := &v1.RayClusterConfig{}
	rayClusterConfig.Provider = reconcileContext.sshClusterConfig.Provider
	rayClusterConfig.Auth = reconcileContext.sshClusterConfig.Auth

	cluster := reconcileContext.Cluster
	imageRegistry := reconcileContext.ImageRegistry

	rayClusterConfig.ClusterName = cluster.Metadata.Name
	rayClusterConfig.Provider.Type = "local"

	if rayClusterConfig.Docker.ContainerName == "" {
		rayClusterConfig.Docker.ContainerName = "ray_container"
	}

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image prefix")
	}

	rayClusterConfig.Docker.Image = util.BuildClusterImageRef(imagePrefix, cluster.Spec.Version, "")
	rayClusterConfig.Docker.PullBeforeRun = true
	// Determine cluster generation: > v1.0.0 uses DOOD engine isolation,
	// <= v1.0.0 mounts NFS inside ray_container and needs elevated privileges.
	isNewCluster, err := semver.LessThan("v1.0.0", cluster.Spec.Version)
	if err != nil {
		klog.Warningf("Failed to parse cluster version %s, assuming new version: %v", cluster.Spec.Version, err)

		isNewCluster = true
	}

	// RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper causes parent processes to lose
	// child exit codes. Ray 2.53.0+ (serving version > v1.0.0) provides RAY_process_group_cleanup_enabled
	// which doesn't have this issue.
	rayProcessCleanupEnv := "-e RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper=true"
	if isNewCluster {
		rayProcessCleanupEnv = "-e RAY_process_group_cleanup_enabled=true"
	}

	// Common options shared by old and new clusters.
	rayClusterConfig.Docker.RunOptions = []string{
		rayProcessCleanupEnv,
		// Reduce Ray object store memory from default 30% to 10%, freeing memory for inference engines
		"-e RAY_DEFAULT_OBJECT_STORE_MEMORY_PROPORTION=0.1",
		// Disable OTEL metrics backend due to metrics loss issue, fall back to OpenCensus
		"-e RAY_enable_open_telemetry=false",
		// Increase nofile ulimit to avoid "Too many open files" error in Ray workers
		"--ulimit nofile=65536:65536",
	}

	if isNewCluster {
		// New clusters (> v1.0.0): DOOD architecture — engine containers run as
		// sibling containers on the host via docker.sock. NFS is mounted by the host
		// Docker daemon (volume-opt), so ray_container does NOT need --privileged.
		rayClusterConfig.Docker.RunOptions = append(rayClusterConfig.Docker.RunOptions,
			// Tell Ray to use Docker as the container runtime for runtime_env.container
			"-e RAY_EXPERIMENTAL_RUNTIME_ENV_CONTAINER_RUNTIME=docker",
			// Mount Docker socket for runtime_env.container support (engine version isolation)
			"--volume /var/run/docker.sock:/var/run/docker.sock",
			// Share host /tmp with Ray container so that temp directories created by Ray's
			// container plugin are visible to sibling engine containers via docker.sock.
			"--volume /tmp:/tmp",
			// Share host PID namespace so that engine containers (which also use --pid=host)
			// can see the raylet process and verify it's alive via RAY_RAYLET_PID.
			"--pid=host",
			// Share host IPC namespace so that Ray container and engine containers can
			// communicate via shared memory (used by Ray Object Store).
			"--ipc=host",
		)
	} else {
		// Old clusters (<= v1.0.0): NFS is mounted inside ray_container via SSH,
		// which requires SYS_ADMIN capability and elevated privileges.
		rayClusterConfig.Docker.RunOptions = append(rayClusterConfig.Docker.RunOptions,
			"--privileged",
			"--cap-add=SYS_ADMIN",
			"--security-opt=seccomp=unconfined",
		)
	}

	headLabel := fmt.Sprintf(`--labels='{"%s":"%s"}'`,
		v1.NeutreeServingVersionLabel, cluster.Spec.Version)
	autoScaleWorkerLabel := fmt.Sprintf(`--labels='{"%s":"%s","%s":"%s"}'`,
		v1.NeutreeNodeProvisionTypeLabel, v1.AutoScaleNodeProvisionType,
		v1.NeutreeServingVersionLabel, cluster.Spec.Version)
	staticWorkerLabel := fmt.Sprintf(`--labels='{"%s":"%s","%s":"%s"}'`,
		v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType,
		v1.NeutreeServingVersionLabel, cluster.Spec.Version)

	// --dashboard-agent-grpc-port and --dashboard-grpc-port are deprecated in Ray 2.53.0 (serving version > v1.0.0)
	includeDeprecatedGrpcFlags := !isNewCluster

	commonArgs := `--disable-usage-stats --node-manager-port=8077 --dashboard-agent-listen-port=52365 ` +
		"--min-worker-port=10002 --max-worker-port=20000 " +
		`--runtime-env-agent-port=56999`
	if includeDeprecatedGrpcFlags {
		commonArgs += " --dashboard-agent-grpc-port=8078"
	}

	commonArgs += fmt.Sprintf(` --metrics-export-port=%d`, v1.RayletMetricsPort)

	headCmdParts := []string{
		`ulimit -n 65536; python /home/ray/start.py --head --port=6379 --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0`,
		commonArgs,
	}
	if includeDeprecatedGrpcFlags {
		headCmdParts = append(headCmdParts, "--dashboard-grpc-port=8079")
	}

	headCmdParts = append(headCmdParts, "--dashboard-port=8265", "--ray-client-server-port=10001", headLabel)

	rayClusterConfig.HeadStartRayCommands = []string{
		"ray stop",
		strings.Join(headCmdParts, " "),
	}
	rayClusterConfig.WorkerStartRayCommands = []string{
		"ray stop",
		strings.Join([]string{
			`ulimit -n 65536; python /home/ray/start.py --address=$RAY_HEAD_IP:6379`,
			commonArgs,
			autoScaleWorkerLabel,
		}, " "),
	}
	rayClusterConfig.StaticWorkerStartRayCommands = []string{
		"ray stop",
		strings.Join([]string{
			`ulimit -n 65536; python /home/ray/start.py --address=$RAY_HEAD_IP:6379`,
			commonArgs,
			staticWorkerLabel,
		}, " "),
	}

	initializationCommands := []string{}
	// login registry.
	username, token, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry auth info")
	}

	host, err := util.GetImageRegistryHost(imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image registry host")
	}

	if username != "" && token != "" {
		dockerLoginCommand := fmt.Sprintf("docker login %s -u '%s' -p '%s'", host, username, token)
		initializationCommands = append(initializationCommands, dockerLoginCommand)

		// For new clusters using DOOD architecture, also run docker login inside the
		// ray container so its Docker CLI can authenticate when pulling engine images
		// via runtime_env container (DOOD uses docker.sock from host).
		if isNewCluster {
			rayClusterConfig.HeadStartRayCommands = append([]string{dockerLoginCommand}, rayClusterConfig.HeadStartRayCommands...)
			rayClusterConfig.WorkerStartRayCommands = append([]string{dockerLoginCommand}, rayClusterConfig.WorkerStartRayCommands...)
			rayClusterConfig.StaticWorkerStartRayCommands = append([]string{dockerLoginCommand}, rayClusterConfig.StaticWorkerStartRayCommands...)
		}
	}

	rayClusterConfig.InitializationCommands = initializationCommands

	// ModelCaches is now in ClusterConfig level
	mutateModelCaches(rayClusterConfig, reconcileContext.Cluster.Spec.Config.ModelCaches)

	return rayClusterConfig, nil
}

// checkHeadNodeHealth checks whether the head node is fully healthy by verifying both
// dashboard API reachability (GCS) and the head node's raylet state, and returns the
// head node's serving version when alive. This avoids redundant ListNodes calls.
// Returns:
//   - (true, version, nil)  — dashboard reachable AND at least one head raylet is ALIVE
//   - (false, "", nil)      — dashboard unreachable, or raylet is not alive
//   - (false, "", err)      — an unexpected error occurred (e.g. ListNodes failed)
func (c *sshRayClusterReconciler) checkHeadNodeHealth(reconcileCtx *ReconcileContext) (bool, string, error) {
	_, err := reconcileCtx.rayService.GetClusterMetadata()
	if err != nil {
		klog.V(4).Infof("Head node dashboard unreachable for cluster %s: %v",
			reconcileCtx.Cluster.Metadata.WorkspaceName(), err)
		return false, "", nil
	}

	nodes, err := reconcileCtx.rayService.ListNodes()
	if err != nil {
		return false, "", errors.Wrap(err, "failed to list ray nodes")
	}

	// Ray can keep multiple records for the same head node across restarts
	// (old DEAD entry + new ALIVE entry). Return alive only if at least one
	// head node record is in AliveNodeState.
	for _, node := range nodes {
		if node.Raylet.IsHeadNode && node.Raylet.State == v1.AliveNodeState {
			return true, v1.GetVersionFromLabels(node.Raylet.Labels), nil
		}
	}

	// No alive head node found. This covers:
	// - Head raylet exited and Ray lost the isHeadNode flag
	// - Head node exists but state is DEAD
	// - No head node in list yet (initial startup; ProvisioningWaitTime prevents rebuild loops)
	return false, "", nil
}

func (c *sshRayClusterReconciler) upgradeCluster(reconcileCtx *ReconcileContext) error {
	oldVersion := reconcileCtx.Cluster.Status.Version
	newVersion := reconcileCtx.Cluster.Spec.Version

	c.logWithProcessMessage(reconcileCtx,
		fmt.Sprintf("Start to upgrade cluster from %s to %s", oldVersion, newVersion))

	// Step 1: Pre-pull images on all nodes before stopping the cluster.
	c.logWithProcessMessage(reconcileCtx, "Pre-pulling images on all nodes")

	err := c.prePullImages(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to pre-pull images")
	}

	c.logWithProcessMessage(reconcileCtx, "Pre-pull completed")

	// Step 2: Force stop all workers and ray down
	c.logWithProcessMessage(reconcileCtx, "Stopping cluster")

	err = c.downCluster(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to down cluster during upgrade")
	}

	c.logWithProcessMessage(reconcileCtx, "Cluster stopped")

	// Step 3: Ray up with new image (restart=true)
	c.logWithProcessMessage(reconcileCtx,
		fmt.Sprintf("Starting head node with new version %s", newVersion))

	headIP, err := c.upCluster(reconcileCtx, true)
	if err != nil {
		return errors.Wrap(err, "failed to up cluster during upgrade")
	}

	c.logWithProcessMessage(reconcileCtx, "Head node started successfully")

	// Step 4: Set head provision status
	err = setNodePrivisionStatus(reconcileCtx, headIP, v1.ProvisionedNodeProvisionStatus, true)
	if err != nil {
		klog.Warningf("Failed to set head node provision status during upgrade: %v", err)
	}

	// Step 5: Reconcile worker nodes with new image
	c.logWithProcessMessage(reconcileCtx, "Starting worker nodes")

	err = c.reconcileWorkerNode(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to reconcile worker nodes during upgrade")
	}

	c.logWithProcessMessage(reconcileCtx, "Cluster upgrade completed")

	return nil
}

// prePullImages pre-pulls the new cluster image and engine images on all cluster nodes.
// The cluster image (neutree-serve:<new_version>) is pre-pulled so upCluster/startNode
// can start instantly. Engine images used by running endpoints are pre-pulled so inference
// instances recover quickly after upgrade.
func (c *sshRayClusterReconciler) prePullImages(reconcileCtx *ReconcileContext) error {
	// Collect engine images from running endpoints
	engineImages, err := c.collectEngineImages(reconcileCtx)
	if err != nil {
		return errors.Wrap(err, "failed to collect engine images")
	}

	// Add the new cluster image (neutree-serve with new version)
	imagePrefix, err := util.GetImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return errors.Wrap(err, "failed to get image prefix")
	}

	// Resolve accelerator image suffix for the cluster image (e.g. "rocm" for AMD GPU).
	imageSuffix := ""
	if reconcileCtx.Cluster.Status != nil && reconcileCtx.Cluster.Status.AcceleratorType != nil {
		imageSuffix = c.acceleratorManager.GetImageSuffix(*reconcileCtx.Cluster.Status.AcceleratorType)
	}

	clusterImage := util.BuildClusterImageRef(imagePrefix, reconcileCtx.Cluster.Spec.Version, imageSuffix)

	imageSet := map[string]struct{}{clusterImage: {}}
	for _, img := range engineImages {
		imageSet[img] = struct{}{}
	}

	images := make([]string, 0, len(imageSet))
	for img := range imageSet {
		images = append(images, img)
	}

	klog.Infof("Pre-pulling %d image(s) on cluster %s: %v",
		len(images), reconcileCtx.Cluster.Metadata.WorkspaceName(), images)

	// Collect all node IPs (head + workers)
	nodeIPs := []string{reconcileCtx.sshClusterConfig.Provider.HeadIP}
	nodeIPs = append(nodeIPs, reconcileCtx.sshClusterConfig.Provider.WorkerIPs...)

	// Pull images on all nodes concurrently
	eg := &errgroup.Group{}

	for _, nodeIP := range nodeIPs {
		ip := nodeIP

		eg.Go(func() error {
			return c.pullImagesOnNode(reconcileCtx, ip, images)
		})
	}

	if err := eg.Wait(); err != nil {
		return errors.Wrap(err, "failed to pull engine images on nodes")
	}

	klog.Infof("Successfully pre-pulled engine images on cluster %s", reconcileCtx.Cluster.Metadata.WorkspaceName())

	return nil
}

// collectEngineImages determines the unique set of engine images that need to be pre-pulled.
// It looks at all running/deploying endpoints on this cluster, resolves their engine images,
// and returns deduplicated full image paths.
func (c *sshRayClusterReconciler) collectEngineImages(reconcileCtx *ReconcileContext) ([]string, error) {
	cluster := reconcileCtx.Cluster

	// List endpoints running on this cluster
	endpoints, err := c.storage.ListEndpoint(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "spec->cluster", Operator: "eq", Value: fmt.Sprintf(`"%s"`, cluster.Metadata.Name)},
			{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, cluster.Metadata.Workspace)},
		},
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list endpoints")
	}

	// Get image prefix from image registry
	imagePrefix, err := util.GetImagePrefix(reconcileCtx.ImageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get image prefix")
	}

	// Collect unique engine images
	imageSet := map[string]struct{}{}

	for i := range endpoints {
		ep := &endpoints[i]

		// Only consider active endpoints
		if ep.Status != nil &&
			ep.Status.Phase != v1.EndpointPhaseRUNNING &&
			ep.Status.Phase != v1.EndpointPhaseDEPLOYING {
			continue
		}

		if ep.Spec == nil || ep.Spec.Engine == nil {
			continue
		}

		// Use the endpoint's own accelerator type, not the cluster's
		acceleratorType := ""
		if ep.Spec.Resources != nil {
			acceleratorType = ep.Spec.Resources.GetAcceleratorType()
		}

		image, err := c.resolveEngineImage(ep, imagePrefix, acceleratorType)
		if err != nil {
			klog.Warningf("Failed to resolve engine image for endpoint %s: %v", ep.Metadata.WorkspaceName(), err)
			continue
		}

		if image != "" {
			imageSet[image] = struct{}{}
		}
	}

	images := make([]string, 0, len(imageSet))
	for img := range imageSet {
		images = append(images, img)
	}

	return images, nil
}

// resolveEngineImage looks up the engine for an endpoint and returns the full image path
// for the cluster's accelerator type.
func (c *sshRayClusterReconciler) resolveEngineImage(endpoint *v1.Endpoint, imagePrefix, acceleratorType string) (string, error) {
	// Look up engine from storage
	engines, err := c.storage.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, endpoint.Spec.Engine.Engine)},
			{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, endpoint.Metadata.Workspace)},
		},
	})
	if err != nil {
		return "", errors.Wrap(err, "failed to list engine")
	}

	if len(engines) == 0 {
		return "", fmt.Errorf("engine %s not found", endpoint.Spec.Engine.Engine)
	}

	engine := &engines[0]

	// Find the matching engine version and resolve image
	for _, version := range engine.Spec.Versions {
		if version.Version != endpoint.Spec.Engine.Version {
			continue
		}

		return util.ResolveEngineImage(version, acceleratorType, imagePrefix)
	}

	return "", nil
}

// pullImagesOnNode pulls the given images on a single node via SSH.
func (c *sshRayClusterReconciler) pullImagesOnNode(reconcileCtx *ReconcileContext, nodeIP string, images []string) error {
	sshCommandArgs := c.buildSSHCommandArgs(reconcileCtx, nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&reconcileCtx.sshRayClusterConfig.Docker, sshCommandArgs)

	for _, image := range images {
		klog.Infof("Pre-pulling engine image %s on node %s", image, nodeIP)

		pullCmd := fmt.Sprintf("docker pull %s", image)

		_, err := dockerCommandRunner.Run(reconcileCtx.Ctx, pullCmd, true, nil, false, nil, "host", "", false)
		if err != nil {
			return errors.Wrapf(err, "failed to pull image %s on node %s", image, nodeIP)
		}
	}

	return nil
}

func mutateModelCaches(sshRayClusterConfig *v1.RayClusterConfig, modelCaches []v1.ModelCache) {
	useModelCache := false

	for _, modelCache := range modelCaches {
		if modelCache.HostPath != nil {
			mountPath := path.Join(v1.DefaultSSHClusterModelCacheMountPath, modelCache.Name)
			hostPath := modelCache.HostPath.Path
			sshRayClusterConfig.Docker.RunOptions = append(sshRayClusterConfig.Docker.RunOptions,
				"--volume "+hostPath+":"+mountPath)
			sshRayClusterConfig.InitializationCommands = append(sshRayClusterConfig.InitializationCommands,
				fmt.Sprintf("mkdir -p %s && chmod 755 %s", hostPath, hostPath))
			useModelCache = true

			continue
		}

		klog.Warning("Now only support HostPath source")
	}

	// Change ownership of the model cache directory to the current user in each node, so that the inference instance can read/write files.
	// After that, the inference instance can easy read/write model files even though the directory is mounted from host.
	// Only do this when model cache is used.
	if useModelCache {
		modifyPermissionCommand := fmt.Sprintf("sudo chown -R $(id -u):$(id -g) %s", v1.DefaultSSHClusterModelCacheMountPath)
		sshRayClusterConfig.HeadStartRayCommands = append([]string{modifyPermissionCommand}, sshRayClusterConfig.HeadStartRayCommands...)
		sshRayClusterConfig.WorkerStartRayCommands = append([]string{modifyPermissionCommand}, sshRayClusterConfig.WorkerStartRayCommands...)
		sshRayClusterConfig.StaticWorkerStartRayCommands = append([]string{modifyPermissionCommand},
			sshRayClusterConfig.StaticWorkerStartRayCommands...)
	}
}
