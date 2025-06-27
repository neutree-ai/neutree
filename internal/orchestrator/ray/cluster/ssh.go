package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/nfs"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/config"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type sshClusterManager struct {
	executor           command.Executor
	imageService       registry.ImageService
	acceleratorManager *accelerator.Manager
	storage            storage.Storage

	configMgr *config.Manager

	cluster          *v1.Cluster
	imageRegistry    *v1.ImageRegistry
	sshClusterConfig *v1.RaySSHProvisionClusterConfig

	config *v1.RayClusterConfig
}

func NewRaySSHClusterManager(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, imageService registry.ImageService, acceleratorManager *accelerator.Manager,
	executor command.Executor, s storage.Storage) (*sshClusterManager, error) {
	manager := &sshClusterManager{
		executor: executor,

		cluster:            cluster,
		imageRegistry:      imageRegistry,
		imageService:       imageService,
		acceleratorManager: acceleratorManager,
		storage:            s,
	}

	sshClusterConfig, err := parseSSHClusterConfig(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse ssh cluster config")
	}

	manager.sshClusterConfig = sshClusterConfig

	err = manager.configClusterAcceleratorType()
	if err != nil {
		return nil, errors.Wrap(err, "failed to config cluster accelerator type")
	}

	rayClusterConfig, err := manager.generateRayClusterConfig()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate ray cluster config")
	}

	manager.config = rayClusterConfig

	if cluster.IsInitialized() {
		err = ensureLocalClusterStateFile(rayClusterConfig)
		if err != nil {
			return nil, errors.Wrap(err, "failed to ensure local cluster state file")
		}
	}

	manager.configMgr = config.NewManager(rayClusterConfig.ClusterName)

	if err := manager.configMgr.Generate(rayClusterConfig); err != nil {
		return nil, errors.Wrap(err, "failed to generate config")
	}

	return manager, nil
}

func (c *sshClusterManager) DownCluster(ctx context.Context) error {
	// first stop all static node
	eg := &errgroup.Group{}

	for i := range c.config.Provider.WorkerIPs {
		nodeIP := c.config.Provider.WorkerIPs[i]

		eg.Go(func() error {
			return c.StopNode(ctx, nodeIP)
		})
	}

	// best effort to stop node, ignore error.
	eg.Wait() //nolint:errcheck

	downArgs := []string{
		"down",
		"-y",
		"-v",
	}

	downArgs = append(downArgs, c.configMgr.ConfigPath())

	output, err := c.executor.Execute(ctx, "ray", downArgs)
	if err != nil {
		return errors.Wrap(err, "failed to down cluster: "+string(output))
	}

	klog.V(4).Infof("Ray cluster down output: %s", string(output))

	// remove local cluster state file to avoid ray cluster start failed.
	localClusterStatePath := filepath.Join(getRayTmpDir(), fmt.Sprintf("cluster-%s.state", c.config.ClusterName))
	if _, err = os.Stat(localClusterStatePath); err == nil {
		if err = os.Remove(localClusterStatePath); err != nil {
			return errors.Wrap(err, "failed to remove local cluster state file")
		}
	}

	err = c.configMgr.Cleanup()
	if err != nil {
		return errors.Wrap(err, "failed to cleanup cluster config")
	}

	return nil
}

func (c *sshClusterManager) Sync(ctx context.Context) error {
	return nil
}

func (c *sshClusterManager) UpCluster(ctx context.Context, restart bool) (string, error) {
	changed, err := c.mutateAcceleratorRuntimeConfig(ctx, c.getHeadIP())
	if err != nil {
		return "", errors.Wrap(err, "failed to mutate accelerator runtime config")
	}

	if changed {
		err = os.Remove(c.configMgr.ConfigPath())
		if err != nil {
			return "", errors.Wrap(err, "failed to remove config file")
		}

		err = c.configMgr.Generate(c.config)
		if err != nil {
			return "", errors.Wrap(err, "failed to generate config")
		}
	}

	validate := []dependencyValidateFunc{
		validateImageRegistryFunc(c.imageRegistry),
		validateClusterImageFunc(c.imageService, c.imageRegistry.Spec.AuthConfig, c.config.Docker.Image),
	}

	for _, validateFunc := range validate {
		if err = validateFunc(); err != nil {
			return "", errors.Wrap(err, "failed to validate dependency")
		}
	}

	upArgs := []string{
		"up",
		"--disable-usage-stats",
		"--no-config-cache",
		"-y",
		"-v",
	}

	if !restart {
		upArgs = append(upArgs, "--no-restart")
	}

	upArgs = append(upArgs, c.configMgr.ConfigPath())

	output, err := c.executor.Execute(ctx, "ray", upArgs)
	if err != nil {
		return "", errors.Wrap(err, "failed to up cluster: "+string(output))
	}

	klog.V(4).Infof("Ray cluster up output: %s", string(output))

	return c.getHeadIP(), nil
}

func (c *sshClusterManager) StartNode(ctx context.Context, nodeIP string) error {
	_, err := c.mutateAcceleratorRuntimeConfig(ctx, nodeIP)
	if err != nil {
		return errors.Wrap(err, "failed to mutate accelerator runtime config")
	}

	validate := []dependencyValidateFunc{
		validateImageRegistryFunc(c.imageRegistry),
		validateClusterImageFunc(c.imageService, c.imageRegistry.Spec.AuthConfig, c.config.Docker.Image),
	}

	for _, validateFunc := range validate {
		if err = validateFunc(); err != nil {
			return errors.Wrap(err, "failed to validate dependency")
		}
	}

	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	env := map[string]interface{}{
		"RAY_HEAD_IP": c.getHeadIP(),
	}

	for _, command := range c.config.InitializationCommands {
		_, err = dockerCommandRunner.Run(ctx, command, true, nil, false, env, "host", "", false)
		if err != nil {
			return errors.Wrap(err, "failed to run command "+command)
		}
	}

	succeed, err := dockerCommandRunner.RunInit(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to run docker runtime init")
	}

	if !succeed {
		return errors.New("failed to run docker runtime init")
	}

	for _, command := range c.config.StaticWorkerStartRayCommands {
		_, err = dockerCommandRunner.Run(ctx, command, true, nil, false, env, "docker", "", false)
		if err != nil {
			return errors.Wrap(err, "failed to run command "+command)
		}
	}

	return nil
}

func (c *sshClusterManager) drainNode(ctx context.Context, nodeID, reason, message string, deadlineRemainSeconds int) error {
	gcsServerURL := c.getHeadIP() + ":6379"
	drainArgs := []string{
		"drain-node",
		"--address=" + gcsServerURL,
		"--node-id=" + nodeID,
		"--reason=" + reason,
		fmt.Sprintf(`--reason-message="%s"`, message),
		"--deadline-remaining-seconds=" + fmt.Sprintf("%d", deadlineRemainSeconds),
	}

	output, err := c.executor.Execute(ctx, "ray", drainArgs)
	if err != nil {
		return errors.Wrap(err, "failed to drain node: "+string(output))
	}

	klog.V(4).Infof("Ray drain-node output: %s", string(output))

	return nil
}

func (c *sshClusterManager) getNodeByIP(ctx context.Context, nodeIP string) (*v1.NodeSummary, error) {
	dashboardService, err := c.GetDashboardService(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get dashboard service")
	}

	rayNodes, err := dashboardService.ListNodes()
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

func (c *sshClusterManager) StopNode(ctx context.Context, nodeIP string) error {
	node, err := c.getNodeByIP(ctx, nodeIP)
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
		err = c.drainNode(ctx, node.Raylet.NodeID, "DRAIN_NODE_REASON_PREEMPTION", "stop node", 600)
		if err != nil {
			return errors.Wrap(err, "failed to drain node "+nodeIP)
		}
	}

	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	dockerInstalled, err := dockerCommandRunner.CheckDockerInstalled(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check docker installed")
	}

	if !dockerInstalled {
		return nil
	}

	containerRunning, err := dockerCommandRunner.CheckContainerStatus(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to check container status")
	}

	if !containerRunning {
		return nil
	}

	_, err = dockerCommandRunner.Run(ctx, "ray stop", true, nil, false, nil, "docker", "", false)
	if err != nil {
		return errors.Wrap(err, "failed to stop ray process")
	}

	_, err = dockerCommandRunner.Run(ctx, "docker stop "+c.config.Docker.ContainerName, true, nil, false, nil, "host", "", false)
	if err != nil {
		return errors.Wrap(err, "failed to stop docker container")
	}

	return nil
}

func (c *sshClusterManager) getHeadIP() string {
	return c.config.Provider.HeadIP
}

func (c *sshClusterManager) GetDesireStaticWorkersIP(_ context.Context) []string {
	return c.config.Provider.WorkerIPs
}

func (c *sshClusterManager) GetDashboardService(_ context.Context) (dashboard.DashboardService, error) {
	return dashboard.NewDashboardService(fmt.Sprintf("http://%s:8265", c.config.Provider.HeadIP)), nil
}

func (c *sshClusterManager) GetServeEndpoint(_ context.Context) (string, error) {
	return fmt.Sprintf("http://%s:8000", c.config.Provider.HeadIP), nil
}

func (c *sshClusterManager) ConnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error {
	dashboardService, err := c.GetDashboardService(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get dashboard service")
	}

	rayNodes, err := dashboardService.ListNodes()
	if err != nil {
		return errors.Wrap(err, "failed to list ray nodes")
	}

	connectIPs := []string{}

	for i := range rayNodes {
		if rayNodes[i].Raylet.State == v1.AliveNodeState {
			connectIPs = append(connectIPs, rayNodes[i].IP)
		}
	}

	for i := range connectIPs {
		nodeIP := connectIPs[i]
		err := c.connectEndpointModel(ctx, modelRegistry, endpoint, nodeIP)

		if err != nil {
			return errors.Wrapf(err, "failed to connect endpoint %s model %s to node %s", endpoint.Key(), modelRegistry.Key(), nodeIP)
		}
	}

	return nil
}

func (c *sshClusterManager) connectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint, nodeIP string) error {
	klog.V(4).Infof("Connect endpoint %s model to node %s", endpoint.Metadata.Name, nodeIP)

	if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		return nil
	}

	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		modelRegistryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return errors.Wrapf(err, "failed to parse model registry url %s", modelRegistry.Spec.Url)
		}

		if modelRegistryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			err = nfs.NewDockerNfsMounter(*dockerCommandRunner).
				MountNFS(ctx, modelRegistryURL.Host+modelRegistryURL.Path, filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
			if err != nil {
				return errors.Wrap(err, "failed to mount nfs")
			}

			return nil
		}

		return fmt.Errorf("unsupported model registry type %s and scheme %s", modelRegistry.Spec.Type, modelRegistryURL.Scheme)
	}

	return fmt.Errorf("unsupported model registry type %s", modelRegistry.Spec.Type)
}

func (c *sshClusterManager) DisconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint) error {
	dashboardService, err := c.GetDashboardService(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to get dashboard service")
	}

	rayNodes, err := dashboardService.ListNodes()
	if err != nil {
		return errors.Wrap(err, "failed to list ray nodes")
	}

	connectIPs := []string{}

	for i := range rayNodes {
		if rayNodes[i].Raylet.State == v1.AliveNodeState {
			connectIPs = append(connectIPs, rayNodes[i].IP)
		}
	}

	for i := range connectIPs {
		nodeIP := connectIPs[i]
		err := c.disconnectEndpointModel(ctx, modelRegistry, endpoint, nodeIP)

		if err != nil {
			return errors.Wrapf(err, "failed to connect endpoint %s model %s to node %s", endpoint.Key(), modelRegistry.Key(), nodeIP)
		}
	}

	return nil
}

func (c *sshClusterManager) disconnectEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint, nodeIP string) error {
	klog.V(4).Infof("Disconnect endpoint %s model to node %s", endpoint.Metadata.Name, nodeIP)

	if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		return nil
	}

	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		modelRegistryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return errors.Wrapf(err, "failed to parse model registry url %s", modelRegistry.Spec.Url)
		}

		if modelRegistryURL.Scheme == "nfs" {
			err = nfs.NewDockerNfsMounter(*dockerCommandRunner).
				Unmount(ctx, filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
			if err != nil {
				return errors.Wrap(err, "failed to mount nfs")
			}

			return nil
		}

		return fmt.Errorf("unsupported model registry type %s and scheme %s", modelRegistry.Spec.Type, modelRegistryURL.Scheme)
	}

	return fmt.Errorf("unsupported model registry type %s", modelRegistry.Spec.Type)
}

func (c *sshClusterManager) buildSSHCommandArgs(nodeIP string) *command_runner.CommonArgs {
	return &command_runner.CommonArgs{
		NodeID: nodeIP,
		SshIP:  nodeIP,
		AuthConfig: v1.Auth{
			SSHUser:       c.config.Auth.SSHUser,
			SSHPrivateKey: c.configMgr.SSHKeyPath(),
		},
		SSHControlPath: "",
		ProcessExecute: c.executor.Execute,
	}
}

func (c *sshClusterManager) configClusterAcceleratorType() error {
	if c.sshClusterConfig.AcceleratorType != nil {
		return nil
	}

	acceleratorType, err := c.detectClusterAcceleratorType()
	if err != nil {
		return errors.Wrapf(err, "failed to detect cluster accelerator type")
	}

	c.sshClusterConfig.AcceleratorType = &acceleratorType
	c.cluster.Spec.Config = c.sshClusterConfig

	return c.storage.UpdateCluster(strconv.Itoa(c.cluster.ID), &v1.Cluster{
		Spec: c.cluster.Spec,
	})
}

func (c *sshClusterManager) detectClusterAcceleratorType() (string, error) {
	detectAcceleratorType := ""

	acceleratorType, err := c.acceleratorManager.GetNodeAcceleratorType(context.Background(), c.sshClusterConfig.Provider.HeadIP, c.sshClusterConfig.Auth)
	if err != nil {
		return "", errors.Wrap(err, "failed to get node accelerator type")
	}

	detectAcceleratorType = acceleratorType

	for _, workerIP := range c.sshClusterConfig.Provider.WorkerIPs {
		acceleratorType, err = c.acceleratorManager.GetNodeAcceleratorType(context.Background(), workerIP, c.sshClusterConfig.Auth)
		if err != nil {
			return "", errors.Wrap(err, "failed to get node accelerator type")
		}

		if detectAcceleratorType == "" {
			detectAcceleratorType = acceleratorType
			continue
		}

		if acceleratorType == "" {
			continue
		}

		if detectAcceleratorType != acceleratorType {
			return "", errors.New("cluster has different accelerator type")
		}
	}

	return detectAcceleratorType, nil
}

func (c *sshClusterManager) mutateAcceleratorRuntimeConfig(ctx context.Context, nodeIP string) (bool, error) {
	runtimeConfig, err := c.acceleratorManager.GetNodeRuntimeConfig(ctx, *c.sshClusterConfig.AcceleratorType, nodeIP, c.config.Auth)
	if err != nil {
		return false, errors.Wrap(err, "failed to get node runtime config")
	}

	changed := false

	if runtimeConfig.ImageSuffix != "" {
		changed = true
		c.config.Docker.Image = c.config.Docker.Image + "-" + runtimeConfig.ImageSuffix
	}

	if runtimeConfig.Runtime != "" {
		changed = true

		c.config.Docker.RunOptions = append(c.config.Docker.RunOptions, "--runtime="+runtimeConfig.Runtime)
	}

	if runtimeConfig.Env != nil {
		changed = true

		for k, v := range runtimeConfig.Env {
			c.config.Docker.RunOptions = append(c.config.Docker.RunOptions, fmt.Sprintf("-e %s=%s", k, v))
		}
	}

	if runtimeConfig.Options != nil {
		changed = true

		for _, v := range runtimeConfig.Options {
			c.config.Docker.RunOptions = append(c.config.Docker.RunOptions, v)
		}
	}

	return changed, nil
}

// setDefaultRayClusterConfig set default ray cluster config.
func (c *sshClusterManager) generateRayClusterConfig() (*v1.RayClusterConfig, error) {
	rayClusterConfig := &v1.RayClusterConfig{}
	rayClusterConfig.Provider = c.sshClusterConfig.Provider
	rayClusterConfig.Auth = c.sshClusterConfig.Auth

	cluster := c.cluster
	imageRegistry := c.imageRegistry

	rayClusterConfig.ClusterName = cluster.Metadata.Name
	rayClusterConfig.Provider.Type = "local"

	if rayClusterConfig.Docker.ContainerName == "" {
		rayClusterConfig.Docker.ContainerName = "ray_container"
	}

	registryURL, err := url.Parse(imageRegistry.Spec.URL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse image registry url "+imageRegistry.Spec.URL)
	}

	rayClusterConfig.Docker.Image = registryURL.Host + "/" + imageRegistry.Spec.Repository + "/neutree-serve:" + cluster.Spec.Version
	rayClusterConfig.Docker.PullBeforeRun = true
	rayClusterConfig.Docker.RunOptions = []string{
		"--privileged",
		"--cap-add=SYS_ADMIN",
		"--security-opt=seccomp=unconfined",
		"-e RAY_kill_child_processes_on_worker_exit_with_raylet_subreaper=true",
	}

	rayClusterConfig.HeadStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`python /home/ray/start.py --head --port=6379 --metrics-export-port=%d --disable-usage-stats --autoscaling-config=~/ray_bootstrap_config.yaml --dashboard-host=0.0.0.0 --labels='{"%s":"%s"}'`, //nolint:lll
			v1.RayletMetricsPort, v1.NeutreeServingVersionLabel, cluster.Spec.Version),
	}
	rayClusterConfig.WorkerStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`python /home/ray/start.py --address=$RAY_HEAD_IP:6379 --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
			v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.AutoScaleNodeProvisionType, v1.NeutreeServingVersionLabel, cluster.Spec.Version),
	}
	rayClusterConfig.StaticWorkerStartRayCommands = []string{
		"ray stop",
		fmt.Sprintf(`python /home/ray/start.py --address=$RAY_HEAD_IP:6379 --metrics-export-port=%d --disable-usage-stats --labels='{"%s":"%s","%s":"%s"}'`,
			v1.RayletMetricsPort, v1.NeutreeNodeProvisionTypeLabel, v1.StaticNodeProvisionType, v1.NeutreeServingVersionLabel, cluster.Spec.Version),
	}

	initializationCommands := []string{}
	// login registry.
	var password string

	switch {
	case imageRegistry.Spec.AuthConfig.Password != "":
		password = imageRegistry.Spec.AuthConfig.Password
	case imageRegistry.Spec.AuthConfig.IdentityToken != "":
		password = imageRegistry.Spec.AuthConfig.IdentityToken
	case imageRegistry.Spec.AuthConfig.RegistryToken != "":
		password = imageRegistry.Spec.AuthConfig.RegistryToken
	}

	if imageRegistry.Spec.AuthConfig.Username != "" && password != "" {
		dockerLoginCommand := fmt.Sprintf("docker login %s -u '%s' -p '%s'", registryURL.Host, imageRegistry.Spec.AuthConfig.Username, password)
		initializationCommands = append(initializationCommands, dockerLoginCommand)
	}

	rayClusterConfig.InitializationCommands = initializationCommands

	return rayClusterConfig, nil
}

func ensureLocalClusterStateFile(config *v1.RayClusterConfig) error {
	rayClusterTmpDir := getRayTmpDir()

	err := os.MkdirAll(rayClusterTmpDir, 0700)
	if err != nil {
		return err
	}

	localClusterStatePath := filepath.Join(rayClusterTmpDir, "cluster-"+config.ClusterName+".state")
	if _, err = os.Stat(localClusterStatePath); err == nil {
		return nil
	}
	// create local cluster state file
	localClusterState := map[string]v1.LocalNodeStatus{}
	localClusterState[config.Provider.HeadIP] = v1.LocalNodeStatus{
		Tags: map[string]string{
			"ray-node-type":   "head",
			"ray-node-status": "up-to-date",
		},
		State: "running",
	}

	localClusterStateContent, err := json.Marshal(localClusterState)
	if err != nil {
		return err
	}

	err = os.WriteFile(localClusterStatePath, localClusterStateContent, 0600)
	if err != nil {
		return err
	}

	return nil
}

func getRayTmpDir() string {
	tmpDir := os.TempDir()

	if os.Getenv("RAY_TMP_DIR") != "" {
		tmpDir = os.Getenv("RAY_TMP_DIR")
	}

	return filepath.Join(tmpDir, "ray")
}
