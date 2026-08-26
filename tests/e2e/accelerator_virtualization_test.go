package e2e

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/component/hami"
	clustervalidation "github.com/neutree-ai/neutree/internal/cluster/validation"
)

const (
	vgpuEndpointMemoryMiB        = "8192"
	vgpuEndpointCorePercent      = "50"
	vgpuEndpointMemoryMiBValue   = int64(8192)
	vgpuEndpointCorePercentValue = int64(50)
	vgpuFullCardCoreUnits        = int64(100)
)

var _ = Describe("K8s Accelerator Virtualization", Ordered,
	Label("cluster", "endpoint", "k8s", "accelerator-virtualization", "hami", "happy-path"), func() {
		var (
			clusterName          string
			endpointName         string
			fullCardEndpointName string
			productName          string
		)

		BeforeAll(func() {
			requireAcceleratorVirtualizationProfile()
			kubeconfig := requireK8sProfile()

			By("Setting up image registry")
			SetupImageRegistry()

			clusterName = "e2e-vgpu-k8s-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]any{
				"name":                               clusterName,
				"kubeconfig":                         kubeconfig,
				"accelerator_virtualization_enabled": true,
			})

			ch := NewClusterHelper()

			By("Applying K8s cluster with accelerator virtualization enabled: " + clusterName)
			r := ch.Apply(yaml)
			ExpectSuccess(r)

			By("Waiting for virtualized cluster Running")
			ch.EventuallyInPhase(clusterName, v1.ClusterPhaseRunning, "", TerminalPhaseTimeout)
		})

		AfterAll(func() {
			if endpointName != "" {
				deleteEndpoint(endpointName)
			}

			if fullCardEndpointName != "" {
				deleteEndpoint(fullCardEndpointName)
			}

			if clusterName != "" {
				teardownCluster(clusterName)
			}
		})

		It("should install accelerator virtualization and expose virtualized cluster resources", func() {
			cluster := eventuallyVirtualizedClusterResourceInfo(clusterName)

			Expect(cluster.Spec).NotTo(BeNil())
			Expect(cluster.Spec.AcceleratorVirtualizationEnabled()).To(BeTrue())
			Expect(cluster.Status.ComponentStatus).To(HaveKey(v1.ComponentStatusAcceleratorVirtualizationKey))

			component := cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey]
			Expect(component.Phase).To(Equal(v1.ComponentPhaseReady))
			Expect(component.Managed).To(BeTrue())
			Expect(component.Version).NotTo(BeEmpty())

			productName = expectNVIDIAVirtualizedClusterResources(cluster)
		})

		It("should render a fail-closed HAMi admission webhook", func() {
			assertHAMiAdmissionWebhookFailClosed(clusterName)
		})

		It("should deploy a vGPU endpoint and expose endpoint resource allocation", func() {
			if profileModelName() == "" {
				Skip("Model name not configured in profile, skipping vGPU endpoint happy path")
			}

			if productName == "" {
				productName = expectNVIDIAVirtualizedClusterResources(eventuallyVirtualizedClusterResourceInfo(clusterName))
			}

			By("Setting up model registry")
			SetupModelRegistry()
			DeferCleanup(TeardownModelRegistry)

			endpointName = "e2e-vgpu-ep-" + Cfg.RunID

			yamlPath := applyEndpoint(endpointName, clusterName,
				withAccelerator(string(v1.AcceleratorTypeNVIDIAGPU), productName),
				withAcceleratorVirtualization(vgpuEndpointMemoryMiB, "", vgpuEndpointCorePercent))
			DeferCleanup(func() {
				if endpointName != "" {
					deleteEndpoint(endpointName)
					endpointName = ""
				}
			})
			DeferCleanup(removeFileIfExists, yamlPath)

			By("Waiting for vGPU endpoint Running")
			waitEndpointRunning(endpointName)

			endpoint := eventuallyEndpointResourceInfo(endpointName)
			Expect(endpoint.Spec).NotTo(BeNil())
			Expect(endpoint.Spec.Resources).NotTo(BeNil())
			Expect(endpoint.Spec.Resources.Accelerator).To(HaveKeyWithValue(
				v1.AcceleratorVirtualizationMemoryMiBKey, vgpuEndpointMemoryMiB))
			Expect(endpoint.Spec.Resources.Accelerator).To(HaveKeyWithValue(
				v1.AcceleratorVirtualizationCorePercentKey, vgpuEndpointCorePercent))

			expectEndpointVGPUResources(endpoint, productName)

			By("Verifying node-agent exposes vGPU endpoint replica accelerator usage")
			assertK8sNodeAgentEndpointAcceleratorMetricsWithVDeviceIndex(clusterName, endpointName, "")

			By("Verifying node-agent writes vGPU endpoint allocation annotations")
			assertK8sEndpointAcceleratorAllocationAnnotations(clusterName, endpointName)
		})

		It("should deploy a full-card endpoint without virtualization resource keys", func() {
			if profileModelName() == "" {
				Skip("Model name not configured in profile, skipping full-card endpoint happy path")
			}

			cluster := eventuallyVirtualizedClusterResourceInfo(clusterName)
			if productName == "" {
				productName = expectNVIDIAVirtualizedClusterResources(cluster)
			}

			memoryMiB := expectNVIDIAProductMemoryMiB(cluster, productName)

			By("Setting up model registry")
			SetupModelRegistry()
			DeferCleanup(TeardownModelRegistry)

			fullCardEndpointName = "e2e-full-gpu-ep-" + Cfg.RunID

			yamlPath := applyEndpoint(fullCardEndpointName, clusterName,
				withAccelerator(string(v1.AcceleratorTypeNVIDIAGPU), productName))
			DeferCleanup(func() {
				if fullCardEndpointName != "" {
					deleteEndpoint(fullCardEndpointName)
					fullCardEndpointName = ""
				}
			})
			DeferCleanup(removeFileIfExists, yamlPath)

			By("Waiting for full-card endpoint Running")
			waitEndpointRunning(fullCardEndpointName)

			endpoint := eventuallyEndpointResourceInfo(fullCardEndpointName)
			Expect(endpoint.Spec).NotTo(BeNil())
			Expect(endpoint.Spec.Resources).NotTo(BeNil())
			Expect(endpoint.Spec.Resources.Accelerator).NotTo(HaveKey(v1.AcceleratorVirtualizationMemoryMiBKey))
			Expect(endpoint.Spec.Resources.Accelerator).NotTo(HaveKey(v1.AcceleratorVirtualizationMemoryPercentKey))
			Expect(endpoint.Spec.Resources.Accelerator).NotTo(HaveKey(v1.AcceleratorVirtualizationCorePercentKey))

			expectEndpointNVIDIAGPUResourcesWithExpected(endpoint, productName, memoryMiB, vgpuFullCardCoreUnits)

			By("Verifying node-agent exposes full-card endpoint replica accelerator usage")
			assertK8sNodeAgentEndpointAcceleratorMetrics(clusterName, fullCardEndpointName)

			By("Verifying node-agent writes full-card endpoint allocation annotations")
			assertK8sEndpointAcceleratorAllocationAnnotations(clusterName, fullCardEndpointName)

			By("Verifying full-card endpoint pod bypasses HAMi CUDA control")
			assertK8sEndpointPodEnvironment(
				clusterName,
				fullCardEndpointName,
				"CUDA_DISABLE_CONTROL",
				"true",
			)
		})

	})

func requireAcceleratorVirtualizationProfile() {
	requireImageRegistryProfile()

	supported, err := clustervalidation.SupportsVirtualizationClusterVersion(profileClusterVersion())
	if err != nil {
		Skip(fmt.Sprintf("Cluster version %q is invalid for accelerator virtualization: %v",
			profileClusterVersion(), err))
	}

	if !supported {
		Skip(fmt.Sprintf("Cluster version %q does not support accelerator virtualization",
			profileClusterVersion()))
	}
}

func assertHAMiAdmissionWebhookFailClosed(clusterName string) {
	ctx := context.Background()
	k8sH := NewK8sHelper(profileKubeconfig())
	clusterNamespace := k8sClusterNamespace(clusterName)

	EventuallyWithOffset(1, func(g Gomega) {
		webhook, err := k8sH.GetMutatingWebhookConfiguration(ctx, hami.WebhookName)
		g.Expect(err).NotTo(HaveOccurred(), "should get HAMi mutating webhook")
		if err != nil {
			return
		}

		g.Expect(webhook.Webhooks).NotTo(BeEmpty(), "HAMi webhook should have entries")
		if len(webhook.Webhooks) == 0 {
			return
		}

		preservesDefaultNamespaceSelector := false
		restrictedToClusterNamespace := false
		for _, entry := range webhook.Webhooks {
			g.Expect(entry.FailurePolicy).NotTo(BeNil(), "HAMi webhook failure policy should be explicit")
			if entry.FailurePolicy != nil {
				g.Expect(*entry.FailurePolicy).To(Equal(admissionregistrationv1.Fail))
			}

			preservesDefaultNamespaceSelector = preservesDefaultNamespaceSelector ||
				hasHAMiDefaultNamespaceSelector(entry.NamespaceSelector)
			restrictedToClusterNamespace = restrictedToClusterNamespace ||
				hasOwningNamespaceSelector(entry.NamespaceSelector, clusterNamespace)
		}

		g.Expect(preservesDefaultNamespaceSelector).To(BeTrue(),
			"HAMi webhook should preserve hami.io/webhook NotIn [ignore]")
		g.Expect(restrictedToClusterNamespace).To(BeFalse(),
			"HAMi webhook must not be restricted to the owning cluster namespace")
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func hasHAMiDefaultNamespaceSelector(selector *metav1.LabelSelector) bool {
	if selector == nil {
		return false
	}

	for _, expression := range selector.MatchExpressions {
		if expression.Key != "hami.io/webhook" || expression.Operator != metav1.LabelSelectorOpNotIn {
			continue
		}

		for _, value := range expression.Values {
			if value == "ignore" {
				return true
			}
		}
	}

	return false
}

func hasOwningNamespaceSelector(selector *metav1.LabelSelector, clusterNamespace string) bool {
	if selector == nil {
		return false
	}

	if selector.MatchLabels["kubernetes.io/metadata.name"] == clusterNamespace {
		return true
	}

	for _, expression := range selector.MatchExpressions {
		if expression.Key != "kubernetes.io/metadata.name" || expression.Operator != metav1.LabelSelectorOpIn {
			continue
		}

		for _, value := range expression.Values {
			if value == clusterNamespace {
				return true
			}
		}
	}

	return false
}

func assertK8sEndpointPodEnvironment(clusterName, endpointName, name, value string) {
	ctx := context.Background()
	k8sH := NewK8sHelper(profileKubeconfig())
	namespace := k8sClusterNamespace(clusterName)
	containerName := profileEngineName()

	EventuallyWithOffset(1, func(g Gomega) {
		pods, err := k8sH.ListPods(ctx, namespace, "endpoint="+endpointName)
		g.Expect(err).NotTo(HaveOccurred(), "should list endpoint pods")
		if err != nil {
			return
		}

		g.Expect(pods).NotTo(BeEmpty(), "endpoint pods should exist")
		if len(pods) == 0 {
			return
		}

		foundContainer := false
		foundEnvironment := false
		for _, pod := range pods {
			for _, container := range pod.Spec.Containers {
				if container.Name != containerName {
					continue
				}

				foundContainer = true
				if hasContainerEnvironment(container, name, value) {
					foundEnvironment = true
				}
			}
		}

		g.Expect(foundContainer).To(BeTrue(),
			"should find engine container %q in endpoint pods", containerName)
		g.Expect(foundEnvironment).To(BeTrue(),
			"engine container should contain %s=%s", name, value)
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())
}

func hasContainerEnvironment(container corev1.Container, name, value string) bool {
	for _, env := range container.Env {
		if env.Name == name && env.Value == value {
			return true
		}
	}

	return false
}

func TestHAMiWebhookSelectorPredicates(t *testing.T) {
	defaultSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      "hami.io/webhook",
			Operator: metav1.LabelSelectorOpNotIn,
			Values:   []string{"ignore"},
		}},
	}

	if !hasHAMiDefaultNamespaceSelector(defaultSelector) {
		t.Fatal("expected HAMi default namespace selector to be detected")
	}
	if hasOwningNamespaceSelector(defaultSelector, "neutree-system") {
		t.Fatal("default selector must not be treated as an owning namespace selector")
	}

	for name, selector := range map[string]*metav1.LabelSelector{
		"match label": {
			MatchLabels: map[string]string{"kubernetes.io/metadata.name": "neutree-system"},
		},
		"match expression": {
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      "kubernetes.io/metadata.name",
				Operator: metav1.LabelSelectorOpIn,
				Values:   []string{"neutree-system"},
			}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !hasOwningNamespaceSelector(selector, "neutree-system") {
				t.Fatal("expected owning namespace selector to be detected")
			}
		})
	}
}

func TestHasContainerEnvironment(t *testing.T) {
	container := corev1.Container{Env: []corev1.EnvVar{{Name: "CUDA_DISABLE_CONTROL", Value: "true"}}}

	if !hasContainerEnvironment(container, "CUDA_DISABLE_CONTROL", "true") {
		t.Fatal("expected matching container environment to be detected")
	}
	if hasContainerEnvironment(container, "CUDA_DISABLE_CONTROL", "false") {
		t.Fatal("environment value mismatch must not match")
	}
}

func eventuallyVirtualizedClusterResourceInfo(clusterName string) v1.Cluster {
	var cluster v1.Cluster

	Eventually(func(g Gomega) {
		cluster = getClusterFullJSON(clusterName)

		g.Expect(cluster.Status).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo.Allocatable).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo.Available).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo.NodeResources).NotTo(BeEmpty())

		group := cluster.Status.ResourceInfo.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
		g.Expect(group).NotTo(BeNil())
		g.Expect(group.Products).NotTo(BeEmpty())

		for _, product := range group.Products {
			if product.Virtualization != nil &&
				product.Virtualization.MemoryMiB > 0 &&
				product.Virtualization.CoreUnits > 0 {
				return
			}
		}

		g.Expect("nvidia gpu virtualization resource").To(Equal("available"))
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return cluster
}

func expectNVIDIAVirtualizedClusterResources(cluster v1.Cluster) string {
	ExpectWithOffset(1, cluster.Status).NotTo(BeNil())
	ExpectWithOffset(1, cluster.Status.ResourceInfo).NotTo(BeNil())

	resources := cluster.Status.ResourceInfo
	allocatableGroup := resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	availableGroup := resources.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	ExpectWithOffset(1, allocatableGroup).NotTo(BeNil())
	ExpectWithOffset(1, availableGroup).NotTo(BeNil())

	productName, allocatableProduct := firstVirtualizedProduct(allocatableGroup)
	ExpectWithOffset(1, productName).NotTo(BeEmpty())
	ExpectWithOffset(1, allocatableProduct.Virtualization.MemoryMiB).To(BeNumerically(">", 0))
	ExpectWithOffset(1, allocatableProduct.Virtualization.CoreUnits).To(BeNumerically(">", 0))

	availableProduct := availableGroup.Products[v1.AcceleratorProduct(productName)]
	ExpectWithOffset(1, availableProduct).NotTo(BeNil())
	ExpectWithOffset(1, availableProduct.Virtualization).NotTo(BeNil())
	ExpectWithOffset(1, availableProduct.Virtualization.MemoryMiB).To(
		BeNumerically("<=", allocatableProduct.Virtualization.MemoryMiB))
	ExpectWithOffset(1, availableProduct.Virtualization.CoreUnits).To(
		BeNumerically("<=", allocatableProduct.Virtualization.CoreUnits))

	deviceCount := expectClusterProductDevices(resources.NodeResources, productName)
	ExpectWithOffset(1, allocatableProduct.Quantity).To(Equal(float64(deviceCount)))

	equivalentAvailableCapacity := expectClusterProductEquivalentAvailableCapacity(
		resources.NodeResources,
		productName,
	)
	groupEquivalentAvailableCapacity := 0.0
	for product := range allocatableGroup.Products {
		groupEquivalentAvailableCapacity += expectClusterProductEquivalentAvailableCapacity(
			resources.NodeResources,
			string(product),
		)
	}
	ExpectWithOffset(1, availableGroup.Quantity).To(
		BeNumerically("~", groupEquivalentAvailableCapacity, 1e-9))
	ExpectWithOffset(1, availableGroup.ProductGroups[v1.AcceleratorProduct(productName)]).To(
		BeNumerically("~", equivalentAvailableCapacity, 1e-9))
	ExpectWithOffset(1, availableProduct.Quantity).To(
		BeNumerically("~", equivalentAvailableCapacity, 1e-9))

	return productName
}

func firstVirtualizedProduct(group *v1.AcceleratorGroup) (string, *v1.AcceleratorProductResource) {
	if group == nil {
		return "", nil
	}

	for productName, product := range group.Products {
		if product != nil && product.Virtualization != nil {
			return string(productName), product
		}
	}

	return "", nil
}

func expectClusterProductDevices(nodes map[string]*v1.NodeResourceStatus, productName string) int {
	count := 0

	for nodeID, node := range nodes {
		for _, device := range node.Devices {
			if device.Product != productName {
				continue
			}

			count++
			ExpectWithOffset(1, device.UUID).NotTo(BeEmpty())
			ExpectWithOffset(1, device.Health).To(BeTrue())
			ExpectWithOffset(1, device.Allocatable).NotTo(BeNil(), "node %s device %s", nodeID, device.UUID)
			ExpectWithOffset(1, device.Allocatable.MemoryMiB).To(BeNumerically(">", 0))
			ExpectWithOffset(1, device.Allocatable.CoreUnits).To(BeNumerically(">", 0))
			ExpectWithOffset(1, device.Available).NotTo(BeNil(), "node %s device %s", nodeID, device.UUID)
			ExpectWithOffset(1, device.Available.MemoryMiB).To(BeNumerically(">=", 0))
			ExpectWithOffset(1, device.Available.CoreUnits).To(BeNumerically(">=", 0))
		}
	}

	ExpectWithOffset(1, count).To(BeNumerically(">", 0))

	return count
}

func expectClusterProductEquivalentAvailableCapacity(
	nodes map[string]*v1.NodeResourceStatus,
	productName string,
) float64 {
	total := 0.0

	for _, node := range nodes {
		for _, device := range node.Devices {
			if device.Product != productName {
				continue
			}

			ExpectWithOffset(1, device.Health).To(BeTrue())
			ExpectWithOffset(1, device.Allocatable).NotTo(BeNil())
			ExpectWithOffset(1, device.Available).NotTo(BeNil())

			total += math.Min(
				clampEquivalentRatio(device.Available.MemoryMiB, device.Allocatable.MemoryMiB),
				clampEquivalentRatio(device.Available.CoreUnits, device.Allocatable.CoreUnits),
			)
		}
	}

	return total
}

func clampEquivalentRatio(available, allocatable int64) float64 {
	if allocatable <= 0 {
		return 0
	}

	return math.Min(math.Max(float64(available)/float64(allocatable), 0), 1)
}

func expectNVIDIAProductMemoryMiB(cluster v1.Cluster, productName string) int64 {
	ExpectWithOffset(1, cluster.Status).NotTo(BeNil())
	ExpectWithOffset(1, cluster.Status.ResourceInfo).NotTo(BeNil())

	metadata := cluster.Status.ResourceInfo.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU]
	ExpectWithOffset(1, metadata).NotTo(BeNil())

	productMetadata := metadata.Products[v1.AcceleratorProduct(productName)]
	ExpectWithOffset(1, productMetadata).NotTo(BeNil())
	ExpectWithOffset(1, productMetadata.MemoryTotalMiB).To(BeNumerically(">", 0))

	return int64(productMetadata.MemoryTotalMiB)
}

func eventuallyEndpointResourceInfo(endpointName string) v1.Endpoint {
	var endpoint v1.Endpoint

	Eventually(func(g Gomega) {
		endpoint = getEndpoint(endpointName)

		g.Expect(endpoint.Status).NotTo(BeNil())
		g.Expect(endpoint.Status.Phase).To(Equal(v1.EndpointPhaseRUNNING))
		g.Expect(endpoint.Status.Resources).NotTo(BeNil())
		g.Expect(endpoint.Status.Resources.Summary).NotTo(BeNil())
		g.Expect(endpoint.Status.Resources.Summary.Products).NotTo(BeEmpty())
		g.Expect(endpoint.Status.Resources.Replicas).NotTo(BeEmpty())
	}, 2*IntermediatePhaseTimeout, 5*time.Second).Should(Succeed())

	return endpoint
}

func expectEndpointVGPUResources(endpoint v1.Endpoint, productName string) {
	expectEndpointNVIDIAGPUResourcesWithExpected(endpoint, productName,
		vgpuEndpointMemoryMiBValue, vgpuEndpointCorePercentValue)
}

func expectEndpointNVIDIAGPUResourcesWithExpected(
	endpoint v1.Endpoint,
	productName string,
	expectedMemoryMiB int64,
	expectedCoreUnits int64,
) {
	resources := endpoint.Status.Resources
	ExpectWithOffset(1, resources).NotTo(BeNil())
	ExpectWithOffset(1, resources.Summary).NotTo(BeNil())

	usage := resources.Summary.Products[v1.AcceleratorProduct(productName)]
	ExpectWithOffset(1, usage).NotTo(BeNil())
	ExpectWithOffset(1, usage.MemoryMiB).To(Equal(expectedMemoryMiB))
	ExpectWithOffset(1, usage.CoreUnits).To(Equal(expectedCoreUnits))

	var memoryMiB, coreUnits int64
	for _, replica := range resources.Replicas {
		ExpectWithOffset(1, replica.ReplicaID).NotTo(BeEmpty())
		ExpectWithOffset(1, replica.NodeID).NotTo(BeEmpty())
		ExpectWithOffset(1, replica.Devices).NotTo(BeEmpty())

		for _, device := range replica.Devices {
			ExpectWithOffset(1, device.UUID).NotTo(BeEmpty())
			ExpectWithOffset(1, device.Product).To(Equal(productName))
			ExpectWithOffset(1, device.NodeID).NotTo(BeEmpty())

			memoryMiB += device.MemoryMiB
			coreUnits += device.CoreUnits
		}
	}

	ExpectWithOffset(1, memoryMiB).To(Equal(usage.MemoryMiB))
	ExpectWithOffset(1, coreUnits).To(Equal(usage.CoreUnits))
}

func removeFileIfExists(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
