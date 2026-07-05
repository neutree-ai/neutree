package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/allocation"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/hami"
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/runtimeusage"
	"github.com/neutree-ai/neutree/internal/version"
)

const (
	clusterTypeKubernetes = "kubernetes"
	clusterTypeRay        = "ray"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		info := version.Get()
		fmt.Println(info.String())
		os.Exit(0)
	}

	klog.InitFlags(nil)
	defer klog.Flush()

	opts := newOptions()
	opts.addFlags(pflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	config, err := opts.config()
	if err != nil {
		klog.Fatalf("Failed to build neutree-node-agent config: %v", err)
	}

	server, err := neutreemetrics.NewServer(config)
	if err != nil {
		klog.Fatalf("Failed to create neutree-node-agent server: %v", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := server.Run(ctx); err != nil {
		klog.Fatalf("Failed to run neutree-node-agent: %v", err)
	}
}

type options struct {
	listenAddress           string
	clusterType             string
	node                    string
	nodeIP                  string
	nodeExporterURL         string
	acceleratorExporterURLs []string
	kubeletPodResourcesSock string
	rayDashboardURL         string
	procFSRoot              string
	cgroupFSRoot            string
}

func newOptions() *options {
	return &options{
		listenAddress:   ":9101",
		clusterType:     clusterTypeKubernetes,
		nodeExporterURL: "http://127.0.0.1:9100/metrics",
		procFSRoot:      "/proc",
		cgroupFSRoot:    "/sys/fs/cgroup",
	}
}

func (o *options) addFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.listenAddress, "listen-address", o.listenAddress, "HTTP listen address")
	fs.StringVar(&o.clusterType, "cluster-type", o.clusterType, "Cluster type used to select allocation and runtime providers")
	fs.StringVar(&o.node, "node", o.node, "Local node name used by Kubernetes and Ray providers")
	fs.StringVar(&o.nodeIP, "node-ip", o.nodeIP, "Local node IP used to match the Ray Dashboard node")
	fs.StringVar(&o.nodeExporterURL, "node-exporter-url", o.nodeExporterURL, "Node exporter metrics URL")
	fs.StringArrayVar(&o.acceleratorExporterURLs, "accelerator-exporter-url", nil,
		"Accelerator exporter metrics URL; can be specified multiple times")
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
	config := neutreemetrics.Config{
		ListenAddress:           o.listenAddress,
		Labels:                  o.labels(),
		NodeExporterURL:         o.nodeExporterURL,
		AcceleratorExporterURLs: o.acceleratorExporterURLs,
	}

	writer, err := o.kubernetesWriter()
	if err != nil {
		return neutreemetrics.Config{}, err
	}

	config.KubernetesWriter = writer
	config.AllocationProvider = o.allocationProvider(writer)
	config.EndpointGPUUsageProvider = o.endpointGPUUsageProvider(writer)
	runtimeUsageProvider, err := o.runtimeUsageProvider(writer)
	if err != nil {
		return neutreemetrics.Config{}, err
	}
	config.RuntimeUsageProvider = runtimeUsageProvider

	return config, nil
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
) allocation.Provider {
	switch o.clusterType {
	case clusterTypeKubernetes:
		if writer == nil {
			return nil
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
		}
	case clusterTypeRay:
		if o.rayDashboardURL == "" {
			return nil
		}

		return allocation.RayServeAllocationProvider{
			DashboardURL: o.rayDashboardURL,
			Node:         o.node,
			NodeIP:       o.nodeIP,
			ProcEnv:      allocation.ProcFSEnvReader{Root: o.procFSRoot},
		}
	default:
		return nil
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
