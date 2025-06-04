package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"k8s.io/klog"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/nfs"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/command_runner"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/config"
	"github.com/neutree-ai/neutree/internal/orchestrator/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/command"
)

type sshClusterManager struct {
	executor  command.Executor
	configMgr *config.Manager
	config    *v1.RayClusterConfig

	cluster       *v1.Cluster
	imageRegistry *v1.ImageRegistry
	imageService  registry.ImageService
}

func NewRaySSHClusterManager(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry, imageService registry.ImageService,
	executor command.Executor) (*sshClusterManager, error) {
	rayClusterConfig, err := generateRayClusterConfig(cluster, imageRegistry)
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate ray cluster config")
	}

	if cluster.IsInitialized() {
		err = ensureLocalClusterStateFile(rayClusterConfig)
		if err != nil {
			return nil, errors.Wrap(err, "failed to ensure local cluster state file")
		}
	}

	configMgr := config.NewManager(rayClusterConfig.ClusterName)

	if err := configMgr.Generate(rayClusterConfig); err != nil {
		return nil, errors.Wrap(err, "failed to generate config")
	}

	manager := &sshClusterManager{
		executor:  executor,
		configMgr: configMgr,
		config:    rayClusterConfig,

		cluster:       cluster,
		imageRegistry: imageRegistry,
		imageService:  imageService,
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

	return nil
}

func (c *sshClusterManager) Sync(ctx context.Context) error {
	return nil
}

func (c *sshClusterManager) UpCluster(ctx context.Context, restart bool) (string, error) {
	image, err := getBaseImage(c.cluster, c.imageRegistry)
	if err != nil {
		return "", errors.Wrapf(err, "failed to get cluster base image for cluster %s", c.cluster.Metadata.Name)
	}

	acceleratorType := c.getNodeAcceleratorType(ctx, c.getHeadIP())
	if acceleratorType != "" && acceleratorType != NvdiaAcceleratorType {
		if suffix, ok := acceleratorImageTagSuffix[acceleratorType]; ok && suffix != "" {
			image = image + "-" + suffix
			c.config.Docker.Image = image
		}

		runtimeOption, err := c.getNodeDockerRuntimeConfiguration(ctx, c.getHeadIP())
		if err != nil {
			return "", errors.Wrap(err, "failed to get node docker runtime configuration")
		}

		if runtimeOption != "" {
			c.config.Docker.RunOptions = append(c.config.Docker.RunOptions, runtimeOption)
		}

		err = os.Remove(c.configMgr.ConfigPath())
		if err != nil {
			return "", errors.Wrap(err, "failed to remove local cluster config")
		}

		err = c.configMgr.Generate(c.config)
		if err != nil {
			return "", errors.Wrap(err, "failed to update local cluster config")
		}
	}

	validate := []dependencyValidateFunc{
		validateImageRegistryFunc(c.imageRegistry),
		validateClusterImageFunc(c.imageService, c.imageRegistry.Spec.AuthConfig, image),
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
	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	image, err := getBaseImage(c.cluster, c.imageRegistry)
	if err != nil {
		return errors.Wrapf(err, "failed to get cluster base image for cluster %s", c.cluster.Metadata.Name)
	}

	acceleratorType := c.getNodeAcceleratorType(ctx, nodeIP)
	if suffix, ok := acceleratorImageTagSuffix[acceleratorType]; ok && suffix != "" {
		image = image + "-" + suffix
	}

	validate := []dependencyValidateFunc{
		validateImageRegistryFunc(c.imageRegistry),
		validateClusterImageFunc(c.imageService, c.imageRegistry.Spec.AuthConfig, image),
	}

	for _, validateFunc := range validate {
		if err = validateFunc(); err != nil {
			return errors.Wrap(err, "failed to validate dependency")
		}
	}

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

func (c *sshClusterManager) getNodeAcceleratorType(ctx context.Context, nodeIP string) string {
	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	_, err := dockerCommandRunner.Run(ctx, "nvidia-smi", false, nil, false, nil, "host", "", false)
	if err == nil {
		return NvdiaAcceleratorType
	}

	npusmiOutput, err := dockerCommandRunner.Run(ctx, "npu-smi info", false, nil, true, nil, "host", "", false)
	if err == nil {
		if strings.Contains(npusmiOutput, "910B") {
			return Ascend910BAcceleratorType
		}

		if strings.Contains(npusmiOutput, "310P") {
			return Ascend310PAcceleratorType
		}
		// default return ascend310p
		return Ascend310PAcceleratorType
	}

	return ""
}

func (c *sshClusterManager) getNodeDockerRuntimeConfiguration(ctx context.Context, nodeIP string) (string, error) {
	sshCommandArgs := c.buildSSHCommandArgs(nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&c.config.Docker, sshCommandArgs)

	npuRuntimeConfiguration := func() (string, error) {
		deviceOutput, err := dockerCommandRunner.Run(ctx, "ls /dev/davinci[0-9]* | awk -F'davinci' '{print $2}'", false, nil, true, nil, "host", "", false)
		if err != nil {
			return "", errors.Wrap(err, "failed to get Ascend devices")
		}

		deviceIds := []string{}

		for _, deviceId := range strings.Split(deviceOutput, "\n") {
			if _, err := strconv.Atoi(deviceId); err == nil {
				deviceIds = append(deviceIds, deviceId)
			}
		}

		return fmt.Sprintf("-e ASCEND_VISIBLE_DEVICES=%s", strings.Join(deviceIds, ",")), nil
	}

	runtimeConfigurationFuncMap := map[string]func() (string, error){
		Ascend910BAcceleratorType: npuRuntimeConfiguration,
		Ascend310PAcceleratorType: npuRuntimeConfiguration,
	}

	if runtimeConfigurationFunc, ok := runtimeConfigurationFuncMap[c.getNodeAcceleratorType(ctx, nodeIP)]; ok {
		return runtimeConfigurationFunc()
	}

	return "", nil
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
		ClusterName:    c.config.ClusterName,
		ProcessExecute: c.executor.Execute,
	}
}

// setDefaultRayClusterConfig set default ray cluster config.
func generateRayClusterConfig(cluster *v1.Cluster, imageRegistry *v1.ImageRegistry) (*v1.RayClusterConfig, error) {
	rayClusterConfig := &v1.RayClusterConfig{}

	rayConfig, err := json.Marshal(cluster.Spec.Config)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(rayConfig, rayClusterConfig)
	if err != nil {
		return nil, err
	}

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
