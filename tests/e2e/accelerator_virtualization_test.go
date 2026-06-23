package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

		// TestRail: C2722839
		It("should reject a vGPU endpoint when requested memory exceeds per-device availability", Label("C2722839", "negative"), func() {
			var maxAvailableMemoryMiB int64
			productName, maxAvailableMemoryMiB = eventuallyHealthyProductAvailableMemoryMiB(clusterName, productName)
			requestedMemoryMiB := maxAvailableMemoryMiB + 1
			endpointName := "e2e-vgpu-overmem-" + Cfg.RunID
			DeferCleanup(func() {
				endpoints, _, code := listEndpointsByName(endpointName)
				if code == http.StatusOK && len(endpoints) > 0 {
					deleteEndpoint(endpointName)
				}
			})

			payload := map[string]any{
				"metadata": map[string]any{
					"name":      endpointName,
					"workspace": profileWorkspace(),
				},
				"spec": map[string]any{
					"cluster": clusterName,
					"resources": map[string]any{
						"gpu": "1",
						"accelerator": map[string]any{
							v1.AcceleratorTypeKey:                      string(v1.AcceleratorTypeNVIDIAGPU),
							v1.AcceleratorProductKey:                   productName,
							v1.AcceleratorVirtualizationMemoryMiBKey:   strconv.FormatInt(requestedMemoryMiB, 10),
							v1.AcceleratorVirtualizationCorePercentKey: vgpuEndpointCorePercent,
						},
					},
				},
			}

			By("Posting an over-capacity vGPU endpoint")
			body, code := callNeutreeAPIWithJSON(http.MethodPost, "/api/v1/endpoints", payload)
			Expect(code).To(Equal(http.StatusBadRequest), "POST response: %s", string(body))

			var response struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Hint    string `json:"hint"`
			}
			Expect(json.Unmarshal(body, &response)).To(Succeed(), "POST response: %s", string(body))
			Expect(response.Code).To(Equal("10220"))
			Expect(response.Message).To(Equal("endpoint accelerator virtualization resources exceed cluster availability"))
			Expect(response.Hint).To(ContainSubstring(productName))

			By("Confirming the rejected endpoint was not persisted")
			endpoints, listBody, listCode := listEndpointsByName(endpointName)
			Expect(listCode).To(Equal(http.StatusOK), "GET endpoint response: %s", string(listBody))
			Expect(endpoints).To(BeEmpty())
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

func eventuallyHealthyProductAvailableMemoryMiB(clusterName string, preferredProduct string) (string, int64) {
	var (
		productName  string
		maxMemoryMiB int64
	)

	Eventually(func(g Gomega) {
		cluster := getClusterFullJSON(clusterName)

		g.Expect(cluster.Status).NotTo(BeNil())
		g.Expect(cluster.Status.ResourceInfo).NotTo(BeNil())

		resources := cluster.Status.ResourceInfo
		productName = preferredProduct
		if productName == "" {
			g.Expect(resources.Allocatable).NotTo(BeNil())

			group := resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
			g.Expect(group).NotTo(BeNil())

			selectedProduct, productResource := firstVirtualizedProduct(group)
			g.Expect(selectedProduct).NotTo(BeEmpty())
			g.Expect(productResource).NotTo(BeNil())
			g.Expect(productResource.Virtualization.MemoryMiB).To(BeNumerically(">", 0))
			g.Expect(productResource.Virtualization.CoreUnits).To(BeNumerically(">", 0))
			productName = selectedProduct
		}

		maxMemoryMiB = maxHealthyProductAvailableMemoryMiB(
			resources.NodeResources,
			productName,
			vgpuEndpointCorePercentValue,
		)
		g.Expect(maxMemoryMiB).To(BeNumerically(">", 0),
			"product %s should have healthy available devices with vGPU telemetry", productName)
	}, TerminalPhaseTimeout, 5*time.Second).Should(Succeed())

	return productName, maxMemoryMiB
}

func maxHealthyProductAvailableMemoryMiB(
	nodes map[string]*v1.NodeResourceStatus,
	productName string,
	minCoreUnits int64,
) int64 {
	var maxMemoryMiB int64

	for _, node := range nodes {
		if node == nil {
			continue
		}

		for _, device := range node.Devices {
			if device == nil || !device.Health || device.Product != productName || device.Available == nil {
				continue
			}

			if device.Available.CoreUnits < minCoreUnits {
				continue
			}

			if device.Available.MemoryMiB > maxMemoryMiB {
				maxMemoryMiB = device.Available.MemoryMiB
			}
		}
	}

	return maxMemoryMiB
}

func listEndpointsByName(endpointName string) ([]v1.Endpoint, []byte, int) {
	GinkgoHelper()

	query := url.Values{}
	query.Set("metadata->>name", "eq."+endpointName)
	query.Set("metadata->>workspace", "eq."+profileWorkspace())

	body, code := callNeutreeAPIWithBody(http.MethodGet, "/api/v1/endpoints?"+query.Encode(), nil)
	if code != http.StatusOK {
		return nil, body, code
	}

	var endpoints []v1.Endpoint
	ExpectWithOffset(1, json.Unmarshal(body, &endpoints)).To(Succeed(), "GET endpoint response: %s", string(body))

	return endpoints, body, code
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
