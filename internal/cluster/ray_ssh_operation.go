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
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/command_runner"
)

func (c *sshRayClusterReconciler) upCluster(reconcileCtx *ReconcileContext, restart bool) (string, error) {
	changed, err := c.mutateAcceleratorRuntimeConfig(reconcileCtx, reconcileCtx.sshClusterConfig.Provider.HeadIP)
	if err != nil {
		return "", errors.Wrap(err, "failed to mutate accelerator runtime config")
	}

	if changed {
		err = reconcileCtx.sshConfigGenerator.Cleanup()
		if err != nil {
			return "", errors.Wrap(err, "failed to cleanup config")
		}

		err = reconcileCtx.sshConfigGenerator.Generate(reconcileCtx.sshRayClusterConfig)
		if err != nil {
			return "", errors.Wrap(err, "failed to generate config")
		}
	}

	upArgs := []string{fmt.Sprintf("RAY_TMPDIR=%s", reconcileCtx.sshConfigGenerator.BasePath()), "ray", "up", "--disable-usage-stats", "--no-config-cache", "-y", "-v"}
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
	_, err := c.mutateAcceleratorRuntimeConfig(reconcileCtx, nodeIP)
	if err != nil {
		return errors.Wrap(err, "failed to mutate accelerator runtime config")
	}

	sshCommandArgs := c.buildSSHCommandArgs(reconcileCtx, nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&reconcileCtx.sshRayClusterConfig.Docker, sshCommandArgs)

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

func (c *sshRayClusterReconciler) mutateAcceleratorRuntimeConfig(reconcileCtx *ReconcileContext, nodeIP string) (bool, error) {
	if reconcileCtx.Cluster.Status == nil || reconcileCtx.Cluster.Status.AcceleratorType == nil {
		return false, errors.New("cluster status or accelerator type is not set")
	}

	runtimeConfig, err := c.acceleratorManager.GetNodeRuntimeConfig(reconcileCtx.Ctx,
		*reconcileCtx.Cluster.Status.AcceleratorType, nodeIP, reconcileCtx.sshClusterConfig.Auth)
	if err != nil {
		return false, errors.Wrap(err, "failed to get node runtime config")
	}

	changed := false

	if runtimeConfig.ImageSuffix != "" {
		changed = true
		reconcileCtx.sshRayClusterConfig.Docker.Image = reconcileCtx.sshRayClusterConfig.Docker.Image + "-" + runtimeConfig.ImageSuffix
	}

	if runtimeConfig.Runtime != "" {
		changed = true

		reconcileCtx.sshRayClusterConfig.Docker.RunOptions = append(reconcileCtx.sshRayClusterConfig.Docker.RunOptions, "--runtime="+runtimeConfig.Runtime)
	}

	if runtimeConfig.Env != nil {
		changed = true

		for k, v := range runtimeConfig.Env {
			reconcileCtx.sshRayClusterConfig.Docker.RunOptions = append(reconcileCtx.sshRayClusterConfig.Docker.RunOptions, fmt.Sprintf("-e %s=%s", k, v))
		}
	}

	if runtimeConfig.Options != nil {
		changed = true

		reconcileCtx.sshRayClusterConfig.Docker.RunOptions = append(reconcileCtx.sshRayClusterConfig.Docker.RunOptions, runtimeConfig.Options...)
	}

	return changed, nil
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

	rayClusterConfig.Docker.Image = imagePrefix + "/neutree/neutree-serve:" + cluster.Spec.Version
	rayClusterConfig.Docker.PullBeforeRun = true
	rayClusterConfig.Docker.RunOptions = []string{
		"--privileged",
		"--cap-add=SYS_ADMIN",
		"--security-opt=seccomp=unconfined",
		"-e RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper=true",
		// Increase nofile ulimit to avoid "Too many open files" error in Ray workers
		"--ulimit nofile=65536:65536",
	}

	rayClusterConfig.HeadStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`ulimit -n 65536; python /home/ray/start.py --head --port=6379 --metrics-export-port=%d --disable-usage-stats --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"%s":"%s"}'`, //nolint:lll
			v1.RayletMetricsPort, v1.NeutreeServingVersionLabel, cluster.Spec.Version),
	}
	rayClusterConfig.WorkerStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`ulimit -n 65536; python /home/ray/start.py --address=$RAY_HEAD_IP:6379 --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
			v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.AutoScaleNodeProvisionType, v1.NeutreeServingVersionLabel, cluster.Spec.Version),
	}
	rayClusterConfig.StaticWorkerStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`ulimit -n 65536; python /home/ray/start.py --address=$RAY_HEAD_IP:6379 --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
			v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType, v1.NeutreeServingVersionLabel, cluster.Spec.Version),
	}

	initializationCommands := []string{}
	// login registry.
	username, password := util.GetImageRegistryAuthInfo(imageRegistry)
	registry := strings.Split(imagePrefix, "/")[0]

	if username != "" && password != "" {
		dockerLoginCommand := fmt.Sprintf("docker login %s -u '%s' -p '%s'", registry, username, password)
		initializationCommands = append(initializationCommands, dockerLoginCommand)
	}

	rayClusterConfig.InitializationCommands = initializationCommands

	mutateModelCaches(rayClusterConfig, reconcileContext.sshClusterConfig.ModelCaches)

	return rayClusterConfig, nil
}

func mutateModelCaches(sshRayClusterConfig *v1.RayClusterConfig, modelCaches []v1.ModelCache) {
	sshRayClusterConfig.Docker.RunOptions = append(sshRayClusterConfig.Docker.RunOptions,
		fmt.Sprintf("-e %s=%s", v1.ModelCacheDirENV, v1.DefaultSSHClusterModelCacheMountPath))

	useModelCache := false

	for _, modelCache := range modelCaches {
		if modelCache.HostPath == nil {
			klog.Warning("Model cache host path is nil, skip")
			continue
		}

		mountPath := path.Join(v1.DefaultSSHClusterModelCacheMountPath, string(modelCache.ModelRegistryType))

		hostPath := modelCache.HostPath.Path
		sshRayClusterConfig.Docker.RunOptions = append(sshRayClusterConfig.Docker.RunOptions,
			"--volume "+hostPath+":"+mountPath)
		sshRayClusterConfig.InitializationCommands = append(sshRayClusterConfig.InitializationCommands,
			fmt.Sprintf("mkdir -p %s && chmod 755 %s", hostPath, hostPath))

		useModelCache = true
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
