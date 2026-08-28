package app

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/runtimeusage"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

type options struct {
	listenAddress                   string
	clusterType                     string
	node                            string
	nodeIP                          string
	acceleratorType                 string
	acceleratorExporterPort         int
	acceleratorExporterPath         string
	acceleratorExporterNamespace    string
	acceleratorExporterSelectorJSON string
	virtualizationMetricsTargetJSON string
	kubeletPodResourcesSock         string
	rayDashboardURL                 string
	procFSRoot                      string
	cgroupFSRoot                    string
}

func newOptions() *options {
	return &options{
		listenAddress:                   ":9101",
		clusterType:                     v1.KubernetesClusterType,
		kubeletPodResourcesSock:         metricskubernetes.DefaultKubeletPodResourcesSocket,
		procFSRoot:                      "/proc",
		cgroupFSRoot:                    "/sys/fs/cgroup",
		acceleratorExporterNamespace:    os.Getenv(v1.AcceleratorExporterNamespaceEnvKey),
		acceleratorExporterSelectorJSON: os.Getenv(v1.AcceleratorExporterPodSelectorEnvKey),
		virtualizationMetricsTargetJSON: os.Getenv(v1.VirtualizationMetricsTargetEnvKey),
	}
}

func (o *options) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.listenAddress, "listen-address", o.listenAddress, "HTTP listen address")
	fs.StringVar(&o.clusterType, "cluster-type", o.clusterType, "Cluster type used to select allocation and runtime providers")
	fs.StringVar(&o.node, "node", o.node, "Local node name used by Kubernetes and Ray providers")
	fs.StringVar(&o.nodeIP, "node-ip", o.nodeIP, "Local node IP used to match the Ray Dashboard node")
	fs.StringVar(&o.acceleratorType, "accelerator-type", o.acceleratorType,
		"Accelerator type selecting the metrics adapter (for example nvidia_gpu)")
	fs.IntVar(&o.acceleratorExporterPort, "accelerator-exporter-port", o.acceleratorExporterPort,
		"Profile-derived accelerator exporter metrics port for an explicit adapter")
	fs.StringVar(&o.acceleratorExporterPath, "accelerator-exporter-metrics-path", o.acceleratorExporterPath,
		"Profile-derived accelerator exporter metrics path for an explicit adapter")
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
	virtualizationMetricsTarget, err := o.virtualizationMetricsTarget()
	if err != nil {
		return neutreemetrics.Config{}, err
	}

	config := neutreemetrics.Config{
		ListenAddress:                  o.listenAddress,
		Labels:                         o.labels(),
		ClusterType:                    o.clusterType,
		AcceleratorType:                o.acceleratorType,
		AcceleratorExporterPort:        o.acceleratorExporterPort,
		AcceleratorExporterMetricsPath: o.acceleratorExporterPath,
		VirtualizationMetricsTarget:    virtualizationMetricsTarget,
	}.WithAccelerators(registry.accelerators()).WithAcceleratorMetricDescriptors(registry.descriptorsCopy())

	var writer *metricskubernetes.AnnotationWriter

	switch o.clusterType {
	case v1.KubernetesClusterType:
		if o.node == "" {
			return neutreemetrics.Config{}, fmt.Errorf("node name is required for kubernetes cluster type")
		}

		kubernetesClient, err := o.kubernetesClient()
		if err != nil {
			return neutreemetrics.Config{}, err
		}

		writer = o.kubernetesWriter(kubernetesClient)
		config.KubernetesWriter = writer

		config.ScrapeTargetProvider, err = o.scrapeTargetProvider(kubernetesClient)
		if err != nil {
			return neutreemetrics.Config{}, err
		}

		config.RuntimeUsageProvider, err = o.kubernetesRuntimeUsageProvider(kubernetesClient)
		if err != nil {
			return neutreemetrics.Config{}, err
		}
	case v1.SSHClusterType:
		config.ScrapeTargetProvider, err = o.scrapeTargetProvider(nil)
		if err != nil {
			return neutreemetrics.Config{}, err
		}

		config.RuntimeUsageProvider = o.staticRuntimeUsageProvider()
	default:
		return neutreemetrics.Config{}, fmt.Errorf("unsupported cluster type: %s", o.clusterType)
	}

	kubernetesEvidenceProvider, staticEvidenceProvider := o.acceleratorEvidenceProviders(writer)
	config.KubernetesAcceleratorEvidenceProvider = kubernetesEvidenceProvider
	config.StaticAcceleratorEvidenceProvider = staticEvidenceProvider

	return config, nil
}

func (o *options) virtualizationMetricsTarget() (*v1.MetricsTargetProfile, error) {
	raw := strings.TrimSpace(o.virtualizationMetricsTargetJSON)
	if raw == "" {
		return nil, nil
	}

	profile := &v1.MetricsTargetProfile{}
	if err := json.Unmarshal([]byte(raw), profile); err != nil {
		return nil, fmt.Errorf("decode virtualization metrics target: %w", err)
	}

	return profile, nil
}

func (o *options) acceleratorExporterSelector() (map[string]string, error) {
	raw := strings.TrimSpace(o.acceleratorExporterSelectorJSON)
	if raw == "" {
		return nil, nil
	}

	selector := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &selector); err != nil {
		return nil, fmt.Errorf("decode accelerator exporter pod selector: %w", err)
	}

	if len(selector) == 0 {
		return nil, nil
	}

	return selector, nil
}

func (o *options) scrapeTargetProvider(
	kubernetesClient client.Client,
) (neutreemetrics.ScrapeTargetProvider, error) {
	switch o.clusterType {
	case v1.KubernetesClusterType:
		acceleratorExporterSelector, err := o.acceleratorExporterSelector()
		if err != nil {
			return nil, err
		}

		return neutreemetrics.KubernetesScrapeTargetProvider{
			Client:                         kubernetesClient,
			NodeName:                       o.node,
			AcceleratorType:                o.acceleratorType,
			AcceleratorExporterPort:        o.acceleratorExporterPort,
			AcceleratorExporterMetricsPath: o.acceleratorExporterPath,
			AcceleratorExporterNamespace:   o.acceleratorExporterNamespace,
			AcceleratorExporterPodSelector: acceleratorExporterSelector,
		}, nil
	case v1.SSHClusterType:
		return neutreemetrics.StaticScrapeTargetProvider{
			AcceleratorType:                o.acceleratorType,
			AcceleratorExporterPort:        o.acceleratorExporterPort,
			AcceleratorExporterMetricsPath: o.acceleratorExporterPath,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported cluster type: %s", o.clusterType)
	}
}

func (o *options) labels() adapter.CanonicalLabels {
	return adapter.CanonicalLabels{
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

func (o *options) kubernetesRuntimeUsageProvider(
	kubernetesClient client.Client,
) (runtimeusage.Provider, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return runtimeusage.KubernetesCAdvisorRuntimeUsageProvider{
		Client:   kubernetesClient,
		NodeName: o.node,
		Scraper: runtimeusage.KubernetesNodeProxyCAdvisorScraper{
			RESTClient: clientset.CoreV1().RESTClient(),
			NodeName:   o.node,
		},
	}, nil
}

func (o *options) staticRuntimeUsageProvider() runtimeusage.Provider {
	if o.rayDashboardURL == "" {
		return nil
	}

	return runtimeusage.RayServeRuntimeUsageProvider{
		DashboardURL: o.rayDashboardURL,
		Node:         o.node,
		NodeIP:       o.nodeIP,
		CGroupUsage: runtimeusage.CGroupFSUsageReader{
			ProcFSRoot:   o.procFSRoot,
			CGroupFSRoot: o.cgroupFSRoot,
		},
	}
}

func (o *options) kubernetesClient() (client.Client, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}

	return client.New(restConfig, client.Options{Scheme: scheme})
}

func (o *options) kubernetesWriter(kubernetesClient client.Client) *metricskubernetes.AnnotationWriter {
	return &metricskubernetes.AnnotationWriter{
		Client:   kubernetesClient,
		NodeName: o.node,
	}
}
