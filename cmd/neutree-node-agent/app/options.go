package app

import (
	"fmt"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/runtimeusage"
)

type options struct {
	listenAddress           string
	clusterType             string
	metricsMode             string
	node                    string
	nodeIP                  string
	acceleratorType         string
	kubeletPodResourcesSock string
	rayDashboardURL         string
	procFSRoot              string
	cgroupFSRoot            string
}

func newOptions() *options {
	return &options{
		listenAddress: ":9101",
		clusterType:   v1.KubernetesClusterType,
		metricsMode:   neutreemetrics.MetricsModeManaged,
		procFSRoot:    "/proc",
		cgroupFSRoot:  "/sys/fs/cgroup",
	}
}

func (o *options) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.listenAddress, "listen-address", o.listenAddress, "HTTP listen address")
	fs.StringVar(&o.clusterType, "cluster-type", o.clusterType, "Cluster type used to select allocation and runtime providers")
	fs.StringVar(&o.metricsMode, "metrics-mode", o.metricsMode, "Metrics exporter mode: managed or external")
	fs.StringVar(&o.node, "node", o.node, "Local node name used by Kubernetes and Ray providers")
	fs.StringVar(&o.nodeIP, "node-ip", o.nodeIP, "Local node IP used to match the Ray Dashboard node")
	fs.StringVar(&o.acceleratorType, "accelerator-type", o.acceleratorType,
		"Accelerator type selecting the metrics adapter (for example nvidia_gpu)")
	fs.StringVar(&o.kubeletPodResourcesSock, "kubelet-pod-resources-socket",
		metricskubernetes.DefaultKubeletPodResourcesSocket,
		"Kubelet pod resources socket path used to discover Kubernetes accelerator allocations")
	fs.StringVar(&o.rayDashboardURL, "ray-dashboard-url", o.rayDashboardURL,
		"Ray dashboard URL used to discover Ray Serve replica accelerator allocations")
	fs.StringVar(&o.procFSRoot, "procfs-root", o.procFSRoot,
		"procfs root used to read Ray actor process environments")
	fs.StringVar(&o.cgroupFSRoot, "cgroupfs-root", o.cgroupFSRoot,
		"cgroupfs root used to read Ray actor container CPU and memory usage")
}

func (o *options) configWithRegistry(registry adapterRegistry) (neutreemetrics.Config, error) {
	config := neutreemetrics.Config{
		ListenAddress:   o.listenAddress,
		Labels:          o.labels(),
		ClusterType:     o.clusterType,
		AcceleratorType: o.acceleratorType,
	}.WithAccelerators(registry.accelerators()).WithAcceleratorMetricDescriptors(registry.descriptorsCopy())

	writer, err := o.kubernetesWriter()
	if err != nil {
		return neutreemetrics.Config{}, err
	}

	config.KubernetesWriter = writer
	config.ScrapeTargetProvider = o.scrapeTargetProvider(writer)
	kubernetesEvidenceProvider, staticEvidenceProvider := o.acceleratorEvidenceProviders(writer)
	config.KubernetesAcceleratorEvidenceProvider = kubernetesEvidenceProvider
	config.StaticAcceleratorEvidenceProvider = staticEvidenceProvider
	configureNVIDIAKubernetesUsage(registry, writer)
	runtimeUsageProvider, err := o.runtimeUsageProvider(writer)

	if err != nil {
		return neutreemetrics.Config{}, err
	}

	config.RuntimeUsageProvider = runtimeUsageProvider

	return config, nil
}

func (o *options) scrapeTargetProvider(
	writer *metricskubernetes.AnnotationWriter,
) neutreemetrics.ScrapeTargetProvider {
	switch o.clusterType {
	case v1.KubernetesClusterType:
		if writer == nil {
			return nil
		}

		return neutreemetrics.KubernetesScrapeTargetProvider{
			Client:      writer.Client,
			MetricsMode: o.metricsMode,
			NodeName:    writer.NodeName,
		}
	case v1.SSHClusterType:
		return neutreemetrics.StaticScrapeTargetProvider{
			MetricsMode: o.metricsMode,
		}
	default:
		return nil
	}
}

func (o *options) labels() model.CanonicalLabels {
	return model.CanonicalLabels{
		ClusterType: o.clusterType,
		Node:        o.node,
		NodeIP:      o.nodeIP,
	}
}

func (o *options) acceleratorEvidenceProviders(
	writer *metricskubernetes.AnnotationWriter,
) (
	neutreemetrics.KubernetesAcceleratorEvidenceProvider,
	neutreemetrics.StaticAcceleratorEvidenceProvider,
) {
	switch o.clusterType {
	case v1.KubernetesClusterType:
		if writer == nil {
			return nil, nil
		}

		kubernetesProvider := allocation.KubernetesAllocationProvider{
			Client:   writer.Client,
			NodeName: writer.NodeName,
			PodResources: metricskubernetes.KubeletPodResourceLister{
				SocketPath: o.kubeletPodResourcesSock,
			},
		}
		return kubernetesProvider, nil
	case v1.SSHClusterType:
		if o.rayDashboardURL == "" {
			return nil, nil
		}

		rayProvider := allocation.RayServeAllocationProvider{
			DashboardURL: o.rayDashboardURL,
			NodeIP:       o.nodeIP,
			ProcEnv:      allocation.ProcFSEnvReader{Root: o.procFSRoot},
		}

		return nil, rayProvider
	default:
		return nil, nil
	}
}

func (o *options) runtimeUsageProvider(
	writer *metricskubernetes.AnnotationWriter,
) (runtimeusage.Provider, error) {
	switch o.clusterType {
	case v1.KubernetesClusterType:
		if writer == nil {
			return nil, nil
		}

		restConfig, err := rest.InClusterConfig()
		if err != nil {
			return nil, err
		}

		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, err
		}

		return runtimeusage.KubernetesCAdvisorRuntimeUsageProvider{
			Client:   writer.Client,
			NodeName: writer.NodeName,
			Scraper: runtimeusage.KubernetesNodeProxyCAdvisorScraper{
				RESTClient: clientset.CoreV1().RESTClient(),
				NodeName:   writer.NodeName,
			},
		}, nil
	case v1.SSHClusterType:
		if o.rayDashboardURL == "" {
			return nil, nil
		}

		return runtimeusage.RayServeRuntimeUsageProvider{
			DashboardURL: o.rayDashboardURL,
			Node:         o.node,
			NodeIP:       o.nodeIP,
			CGroupUsage: runtimeusage.CGroupFSUsageReader{
				ProcFSRoot:   o.procFSRoot,
				CGroupFSRoot: o.cgroupFSRoot,
			},
		}, nil
	default:
		return nil, nil
	}
}

func (o *options) kubernetesWriter() (*metricskubernetes.AnnotationWriter, error) {
	if o.clusterType != v1.KubernetesClusterType {
		return nil, nil
	}

	if o.node == "" {
		return nil, fmt.Errorf("node name is required for kubernetes cluster type")
	}

	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	kubernetesClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}

	return &metricskubernetes.AnnotationWriter{
		Client:   kubernetesClient,
		NodeName: o.node,
	}, nil
}
