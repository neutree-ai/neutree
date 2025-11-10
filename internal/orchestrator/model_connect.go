package orchestrator

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

func (o *RayOrchestrator) connectKubernetesClusterEndpointModel(modelRegistry v1.ModelRegistry, endpoint v1.Endpoint, op string) error {
	klog.V(4).Infof("Connect endpoint %s model in kubernetes cluster", endpoint.Metadata.Name)

	if modelRegistry.Spec.Type == v1.HuggingFaceModelRegistryType {
		return nil
	}

	ctrlClient, err := util.GetClientFromCluster(o.cluster)
	if err != nil {
		return errors.Wrap(err, "failed to get k8s client from cluster")
	}

	kubeconfig, err := util.GetKubeConfigFromCluster(o.cluster)
	if err != nil {
		return errors.Wrap(err, "failed to get kubeconfig from cluster")
	}

	ctx := context.Background()
	ns := util.ClusterNamespace(o.cluster)

	podList := &corev1.PodList{}
	err = ctrlClient.List(ctx, podList, client.InNamespace(ns), client.MatchingLabels{
		"ray.io/cluster": o.cluster.Metadata.Name,
	})

	if err != nil {
		return errors.Wrap(err, "failed to list pods")
	}

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}

		err = o.connectKubernetesPodEndpointModel(ctx, modelRegistry, endpoint, pod.Name, ns, kubeconfig, op)
		if err != nil {
			return errors.Wrap(err, "failed to disconnect endpoint model")
		}
	}

	return nil
}

func (o *RayOrchestrator) connectKubernetesPodEndpointModel(ctx context.Context, modelRegistry v1.ModelRegistry, endpoint v1.Endpoint,
	podName, ns string, kubeconfig string, op string) error {
	commandRunner := command_runner.NewKubernetesCommandRunner(kubeconfig, podName, ns, "ray-container")

	if modelRegistry.Spec.Type == v1.BentoMLModelRegistryType {
		modelRegistryURL, err := url.Parse(modelRegistry.Spec.Url)
		if err != nil {
			return errors.Wrapf(err, "failed to parse model registry url: %s", modelRegistry.Spec.Url)
		}

		if modelRegistryURL.Scheme == v1.BentoMLModelRegistryConnectTypeNFS {
			mounter := nfs.NewKubernetesNfsMounter(*commandRunner)

			switch op {
			case connect:
				err = mounter.MountNFS(ctx, modelRegistryURL.Host+modelRegistryURL.Path, filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
			case disconnect:
				err = mounter.Unmount(ctx, filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
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

	return nil
}

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

	defer os.Remove(filepath.Dir(sshKeyPath))
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
					filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
			case disconnect:
				err = mounter.Unmount(context.Background(), filepath.Join("/mnt", endpoint.Key(), modelRegistry.Key(), endpoint.Spec.Model.Name))
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
