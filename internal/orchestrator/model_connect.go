package orchestrator

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/nfs"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/command"
	"github.com/neutree-ai/neutree/pkg/command_runner"
)

const (
	connect    = "connect"
	disconnect = "disconnect"
)

func (o *RayOrchestrator) connectSSHClusterEndpointModel(modelRegistry v1.ModelRegistry, endpoint v1.Endpoint, op string) error {
	dashboardService, err := o.getDashboardService()
	if err != nil {
		return errors.Wrap(err, "failed to get dashboard service")
	}

	sshClusterConfig, err := util.ParseSSHClusterConfig(o.cluster)
	if err != nil {
		return errors.Wrap(err, "failed to parse ssh cluster config")
	}

	sshKeyPath, err := util.GenerateTmpSSHKeyFile(sshClusterConfig.Auth.SSHPrivateKey)
	if err != nil {
		return errors.Wrap(err, "failed to generate tmp ssh key file")
	}

	defer os.RemoveAll(filepath.Dir(sshKeyPath))
	// override the private key path
	sshClusterConfig.Auth.SSHPrivateKey = sshKeyPath

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
		err := o.connectSSHNodeEndpointModel(sshClusterConfig, modelRegistry, endpoint, nodeIP, op)

		if err != nil {
			return errors.Wrapf(err, "failed to connect endpoint %s model %s to node %s", endpoint.Key(), modelRegistry.Key(), nodeIP)
		}
	}

	return nil
}

func (o *RayOrchestrator) connectSSHNodeEndpointModel(config *v1.RaySSHProvisionClusterConfig, modelRegistry v1.ModelRegistry,
	endpoint v1.Endpoint, nodeIP string, op string) error {
	klog.V(4).Infof("Connect endpoint %s model to node %s", endpoint.Metadata.Name, nodeIP)

	if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		return nil
	}

	sshCommandArgs := o.buildSSHCommandArgs(config, nodeIP)
	dockerCommandRunner := command_runner.NewDockerCommandRunner(&v1.Docker{
		ContainerName: "ray_container",
	}, sshCommandArgs)

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		modelRegistryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return errors.Wrapf(err, "failed to parse model registry url %s", modelRegistry.Spec.Url)
		}

		if modelRegistryURL.Scheme == "nfs" {
			mounter := nfs.NewDockerNfsMounter(*dockerCommandRunner)

			switch op {
			case connect:
				err = mounter.MountNFS(context.Background(), modelRegistryURL.Host+modelRegistryURL.Path,
					filepath.Join("/mnt", endpoint.Metadata.Workspace, endpoint.Metadata.Name))
			case disconnect:
				err = mounter.Unmount(context.Background(), filepath.Join("/mnt", endpoint.Metadata.Workspace, endpoint.Metadata.Name))
			default:
				return fmt.Errorf("unsupported operation %s", op)
			}

			if err != nil {
				return errors.Wrap(err, "failed to mount nfs")
			}

			return nil
		}

		return fmt.Errorf("unsupported model registry type %s and scheme %s", modelRegistry.Spec.Type, modelRegistryURL.Scheme)
	}

	return fmt.Errorf("unsupported model registry type %s", modelRegistry.Spec.Type)
}

func (o *RayOrchestrator) buildSSHCommandArgs(config *v1.RaySSHProvisionClusterConfig, nodeIP string) *command_runner.CommonArgs {
	args := &command_runner.CommonArgs{
		NodeID: nodeIP,
		SshIP:  nodeIP,
		AuthConfig: v1.Auth{
			SSHUser:       config.Auth.SSHUser,
			SSHPrivateKey: config.Auth.SSHPrivateKey,
		},
		SSHControlPath: "",
	}

	executor := &command.OSExecutor{}
	args.ProcessExecute = executor.Execute

	return args
}
