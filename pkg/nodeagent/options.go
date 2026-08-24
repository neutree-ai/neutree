package nodeagent

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/hami"
	metricskubernetes "github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/model"
	"github.com/neutree-ai/neutree/pkg/nodeagent/internal/neutreemetrics/runtimeusage"
)

const (
	clusterTypeKubernetes = "kubernetes"
	clusterTypeRay        = "ray"
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
		clusterType:   clusterTypeKubernetes,
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
		"Accelerator type selecting the metrics adapter (for example nvidia_gpu); empty uses the legacy DCGM path")
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

func (o *options) config() (neutreemetrics.Config, error) {
	registry, err := newAdapterRegistry(DefaultAdapters())
	if err != nil {
		return neutreemetrics.Config{}, err
	}

	return o.configWithRegistry(registry)
}

func (o *options) configWithRegistry(registry adapterRegistry) (neutreemetrics.Config, error) {
	if o.acceleratorType != "" {
		if _, ok := registry.byType[o.acceleratorType]; !ok {
			return neutreemetrics.Config{}, fmt.Errorf(
				"accelerator adapter %q is not registered; available adapters: %s",
				o.acceleratorType,
				registeredAdapterTypes(registry),
			)
		}
	}

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
	allocationProvider, kubernetesEvidenceProvider, staticEvidenceProvider := o.allocationProvider(writer)
	config.AllocationProvider = allocationProvider
	config.KubernetesAcceleratorEvidenceProvider = kubernetesEvidenceProvider
	config.StaticAcceleratorEvidenceProvider = staticEvidenceProvider
	config.EndpointGPUUsageProvider = o.endpointGPUUsageProvider(writer)
	runtimeUsageProvider, err := o.runtimeUsageProvider(writer)

	if err != nil {
		return neutreemetrics.Config{}, err
	}

	config.RuntimeUsageProvider = runtimeUsageProvider

	return config, nil
}

func registeredAdapterTypes(registry adapterRegistry) string {
	return strings.Join(registry.types(), ", ")
}

func (o *options) scrapeTargetProvider(
	writer *metricskubernetes.AnnotationWriter,
) neutreemetrics.ScrapeTargetProvider {
	switch o.clusterType {
	case clusterTypeKubernetes:
		if writer == nil {
			return nil
		}

		return neutreemetrics.KubernetesScrapeTargetProvider{
			Client:      writer.Client,
			MetricsMode: o.metricsMode,
			NodeName:    writer.NodeName,
		}
	case clusterTypeRay:
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

func (o *options) allocationProvider(
	writer *metricskubernetes.AnnotationWriter,
) (
	allocation.Provider,
	neutreemetrics.KubernetesAcceleratorEvidenceProvider,
	neutreemetrics.StaticAcceleratorEvidenceProvider,
) {
	switch o.clusterType {
	case clusterTypeKubernetes:
		if writer == nil {
			return nil, nil, nil
		}

		kubernetesProvider := allocation.KubernetesAllocationProvider{
			Client:   writer.Client,
			NodeName: writer.NodeName,
			PodResources: metricskubernetes.KubeletPodResourceLister{
				SocketPath: o.kubeletPodResourcesSock,
			},
		}
		hamiProvider := hami.KubernetesProvider{
			Client:   writer.Client,
			NodeName: writer.NodeName,
		}

		return allocation.MultiProvider{
			Providers: []allocation.Provider{kubernetesProvider, hamiProvider},
		}, kubernetesProvider, nil
	case clusterTypeRay:
		if o.rayDashboardURL == "" {
			return nil, nil, nil
		}

		rayProvider := allocation.RayServeAllocationProvider{
			DashboardURL: o.rayDashboardURL,
			Node:         o.node,
			NodeIP:       o.nodeIP,
			ProcEnv:      allocation.ProcFSEnvReader{Root: o.procFSRoot},
		}

		return rayProvider, nil, rayProvider
	default:
		return nil, nil, nil
	}
}

func (o *options) endpointGPUUsageProvider(
	writer *metricskubernetes.AnnotationWriter,
) neutreemetrics.EndpointGPUUsageProvider {
	switch o.clusterType {
	case clusterTypeKubernetes:
		if writer == nil {
			return nil
		}

		return hami.KubernetesProvider{
			Client:   writer.Client,
			NodeName: writer.NodeName,
		}
	default:
		return nil
	}
}

func (o *options) runtimeUsageProvider(
	writer *metricskubernetes.AnnotationWriter,
) (runtimeusage.Provider, error) {
	switch o.clusterType {
	case clusterTypeKubernetes:
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
	case clusterTypeRay:
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
	if o.clusterType != clusterTypeKubernetes {
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
