package e2e

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func assertK8sMetricsResources(
	ctx context.Context,
	k8sH *K8sHelper,
	namespace string,
	clusterVersion string,
) {
	_, err := k8sH.GetDeployment(ctx, namespace, "vmagent")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent deployment should exist")

	_, err = k8sH.GetConfigMap(ctx, namespace, "vmagent-config")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent-config ConfigMap should exist")

	_, err = k8sH.GetServiceAccount(ctx, namespace, "vmagent-service-account")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent ServiceAccount should exist")

	_, err = k8sH.GetRole(ctx, namespace, "vmagent-pod-reader")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent Role should exist")

	_, err = k8sH.GetRoleBinding(ctx, namespace, "vmagent-rolebinding")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent RoleBinding should exist")

	vmagentConfig, err := k8sH.GetConfigMap(ctx, namespace, "vmagent-config")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent-config ConfigMap should exist")

	if !clusterVersionSupportsManagedMetricsExporters(clusterVersion) {
		_, err = k8sH.GetDaemonSet(ctx, namespace, "neutree-node-exporter")
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "node-exporter should not exist before cluster version v1.1.0")
		_, err = k8sH.GetDaemonSet(ctx, namespace, "neutree-node-agent")
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "node-agent should not exist before cluster version v1.1.0")
		_, err = k8sH.GetDaemonSet(ctx, namespace, "nvidia-gpu-dcgm-exporter")
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "DCGM exporter should not exist before cluster version v1.1.0")
		ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).NotTo(ContainSubstring("job_name: 'node-exporter-http'"))
		ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).NotTo(ContainSubstring("job_name: 'neutree-node-agent'"))
		ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).NotTo(ContainSubstring("job_name: 'dcgm-exporter'"))

		assertK8sKubeStateMetricsResources(ctx, k8sH, namespace, clusterVersion)

		return
	}

	By("Checking node-exporter DaemonSet")

	nodeExporter := eventuallyDaemonSetReady(ctx, k8sH, namespace, "neutree-node-exporter")
	ExpectWithOffset(1, nodeExporter.Spec.Template.Spec.Containers).NotTo(BeEmpty())
	ExpectWithOffset(1, nodeExporter.Spec.Template.Spec.Containers[0].Ports).To(ContainElement(
		HaveField("ContainerPort", int32(19100)),
	))
	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("job_name: 'node-exporter-http'"))

	By("Checking neutree-node-agent DaemonSet")

	nodeAgent := eventuallyDaemonSetReady(ctx, k8sH, namespace, "neutree-node-agent")
	ExpectWithOffset(1, nodeAgent.Spec.Template.Spec.Containers).NotTo(BeEmpty())
	nodeAgentContainer := nodeAgent.Spec.Template.Spec.Containers[0]
	ExpectWithOffset(1, nodeAgentContainer.Ports).To(ContainElement(
		HaveField("ContainerPort", int32(19101)),
	))
	ExpectWithOffset(1, nodeAgentContainer.Args).To(ContainElement("--listen-address=:19101"))
	ExpectWithOffset(1, nodeAgentContainer.Args).To(ContainElement("--cluster-type=kubernetes"))
	ExpectWithOffset(1, nodeAgentContainer.Args).To(ContainElement("--metrics-mode=managed"))
	ExpectWithOffset(1, nodeAgentContainer.Args).To(ContainElement("--node=$(NODE_NAME)"))
	ExpectWithOffset(1, nodeAgentContainer.Args).To(ContainElement("--node-ip=$(NODE_IP)"))
	ExpectWithOffset(1, nodeAgentContainer.Args).To(ContainElement("--kubelet-pod-resources-socket=/var/lib/kubelet/pod-resources/kubelet.sock"))
	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("job_name: 'neutree-node-agent'"))
	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("replacement: $1:19101"))

	nodeAgentPod := eventuallyDaemonSetPodReady(ctx, k8sH, namespace, "neutree-node-agent")
	health, err := k8sH.PodProxyGetRaw(ctx, namespace, nodeAgentPod.Name, "19101", "/health")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "node-agent /health should be reachable")
	ExpectWithOffset(1, string(health)).To(ContainSubstring(`"status":"ok"`))

	metrics := eventuallyPodProxyBodyContains(ctx, k8sH, namespace, nodeAgentPod.Name, "19101", "/metrics",
		"neutree_node_ready",
		"neutree_metrics_scrape_up",
		"neutree_node_memory_total_bytes",
	)
	ExpectWithOffset(1, metrics).To(ContainSubstring("neutree_node_cpu_seconds_total"))

	for _, name := range obsoleteEndpointAcceleratorMetricNames {
		ExpectWithOffset(1, metrics).NotTo(ContainSubstring(name))
	}

	ExpectWithOffset(1, metrics).NotTo(ContainSubstring("neutree_gpu_"))
	ExpectWithOffset(1, metrics).NotTo(ContainSubstring("neutree_node_gpu_"))

	gpuNodes, err := k8sH.ListNodes(ctx, "nvidia.com/gpu.present=true")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "should list NVIDIA GPU nodes")

	if len(gpuNodes) == 0 {
		_, err = k8sH.GetDaemonSet(ctx, namespace, "nvidia-gpu-dcgm-exporter")
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "DCGM exporter should not exist without matching GPU nodes")
	} else {
		By("Checking NVIDIA DCGM exporter DaemonSet")

		dcgm := eventuallyDaemonSetReady(ctx, k8sH, namespace, "nvidia-gpu-dcgm-exporter")
		ExpectWithOffset(1, dcgm.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue("nvidia.com/gpu.present", "true"))
		ExpectWithOffset(1, dcgm.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		ExpectWithOffset(1, dcgm.Spec.Template.Spec.Containers[0].Ports).To(ContainElement(
			HaveField("ContainerPort", int32(19400)),
		))
		ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("job_name: 'accelerator-exporter-nvidia-gpu'"))

		By("Checking node-agent writes node accelerator device annotations")
		assertK8sNodeAcceleratorDeviceAnnotations(ctx, k8sH)
	}

	assertK8sKubeStateMetricsResources(ctx, k8sH, namespace, clusterVersion)
}

func assertK8sExternalAcceleratorExporterResources(
	ctx context.Context,
	k8sH *K8sHelper,
	namespace string,
	clusterVersion string,
) {
	_, err := k8sH.GetDeployment(ctx, namespace, "vmagent")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent deployment should exist")

	vmagentConfig, err := k8sH.GetConfigMap(ctx, namespace, "vmagent-config")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "vmagent-config ConfigMap should exist")

	_, err = k8sH.GetDaemonSet(ctx, namespace, "nvidia-gpu-dcgm-exporter")
	ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(),
		"managed DCGM exporter should not exist in external accelerator exporter mode")

	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("job_name: 'dcgm-exporter'"))
	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("label: app=nvidia-dcgm-exporter"))
	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("replacement: $1:9400"))
	ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).NotTo(ContainSubstring("label: app=nvidia-gpu-dcgm-exporter"))

	if clusterVersionSupportsManagedMetricsExporters(clusterVersion) {
		nodeExporter := eventuallyDaemonSetReady(ctx, k8sH, namespace, "neutree-node-exporter")
		ExpectWithOffset(1, nodeExporter.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).To(ContainSubstring("job_name: 'node-exporter-http'"))

		nodeAgent := eventuallyDaemonSetReady(ctx, k8sH, namespace, "neutree-node-agent")
		ExpectWithOffset(1, nodeAgent.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		ExpectWithOffset(1, nodeAgent.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--metrics-mode=external"))
		ExpectWithOffset(1, nodeAgent.Spec.Template.Spec.Containers[0].Args).NotTo(ContainElement(
			ContainSubstring("--accelerator-exporter-url="),
		))
	} else {
		_, err = k8sH.GetDaemonSet(ctx, namespace, "neutree-node-exporter")
		ExpectWithOffset(1, apierrors.IsNotFound(err)).To(BeTrue(), "node-exporter should not exist before cluster version v1.1.0")
		ExpectWithOffset(1, vmagentConfig.Data["prometheus.yml"]).NotTo(ContainSubstring("job_name: 'node-exporter-http'"))
	}

	assertK8sKubeStateMetricsResources(ctx, k8sH, namespace, clusterVersion)
}

func assertK8sKubeStateMetricsResources(
	ctx context.Context,
	k8sH *K8sHelper,
	namespace string,
	clusterVersion string,
) {
	if !clusterVersionSupportsKubeStateMetrics(clusterVersion) {
		_, err := k8sH.GetDeployment(ctx, namespace, "neutree-kube-state-metrics")
		ExpectWithOffset(1, err).To(HaveOccurred(), "kube-state-metrics deployment should not exist before cluster version v1.1.0")

		return
	}

	_, err := k8sH.GetDeployment(ctx, namespace, "neutree-kube-state-metrics")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kube-state-metrics deployment should exist")

	_, err = k8sH.GetService(ctx, namespace, "neutree-kube-state-metrics")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kube-state-metrics Service should exist")

	_, err = k8sH.GetServiceAccount(ctx, namespace, "neutree-kube-state-metrics")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kube-state-metrics ServiceAccount should exist")

	_, err = k8sH.GetRole(ctx, namespace, "neutree-kube-state-metrics")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kube-state-metrics Role should exist")

	_, err = k8sH.GetRoleBinding(ctx, namespace, "neutree-kube-state-metrics")
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "kube-state-metrics RoleBinding should exist")
}
