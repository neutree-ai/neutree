package e2e_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/test/e2e/framework"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("Quick Start K8s", Ordered, Label("quick-k8s"), func() {
	var (
		imageRegistryName = "e2e-k8s-docker-hub"
		clusterName       = "e2e-k8s-cluster"
		modelRegistryName = "e2e-k8s-huggingface"
		endpointName      = "e2e-k8s-endpoint"
		apiKeyName        = "e2e-k8s-api-key"
	)

	AfterAll(func() {
		GinkgoWriter.Println("Cleaning up K8s test resources...")
		client.CleanupResourcesIgnoreErrors(cfg.Workspace, framework.CleanupList{
			Endpoints:       []string{endpointName},
			Clusters:        []string{clusterName},
			ModelRegistries: []string{modelRegistryName},
			ImageRegistries: []string{imageRegistryName},
		})
	})

	It("should complete the quick start workflow on Kubernetes", func() {
		By("Creating image registry and waiting for Connected phase")
		GinkgoWriter.Printf("Creating image registry: %s\n", imageRegistryName)

		ir := &v1.ImageRegistry{
			Metadata: &v1.Metadata{
				Name:      imageRegistryName,
				Workspace: cfg.Workspace,
			},
			Spec: &v1.ImageRegistrySpec{
				URL:        "https://docker.io",
				Repository: "",
			},
		}

		_, err := client.CreateImageRegistry(ir)
		Expect(err).NotTo(HaveOccurred(), "Failed to create image registry")

		err = client.WaitForImageRegistry(cfg.Workspace, imageRegistryName,
			v1.ImageRegistryPhaseCONNECTED, framework.WaitOptions{Timeout: 2 * time.Minute})
		Expect(err).NotTo(HaveOccurred(), "Image registry did not reach Connected phase")
		GinkgoWriter.Println("Image registry is ready")

		By("Creating Kubernetes cluster and waiting for Running phase")
		GinkgoWriter.Printf("Creating K8s cluster: %s\n", clusterName)

		cluster := &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      clusterName,
				Workspace: cfg.Workspace,
			},
			Spec: &v1.ClusterSpec{
				Type:          v1.KubernetesClusterType,
				ImageRegistry: imageRegistryName,
				Config: &v1.ClusterConfig{
					KubernetesConfig: &v1.KubernetesClusterConfig{
						Kubeconfig: cfg.Kubeconfig,
						Router: v1.RouterSpec{
							AccessMode: v1.KubernetesAccessMode(cfg.K8sAccessMode),
							Replicas:   1,
						},
					},
					ModelCaches: []v1.ModelCache{
						{
							Name: v1.DefaultModelCacheRelativePath,
							HostPath: &corev1.HostPathVolumeSource{
								Path: cfg.ModelCachePath,
							},
						},
					},
				},
			},
		}

		_, err = client.CreateCluster(cluster)
		Expect(err).NotTo(HaveOccurred(), "Failed to create K8s cluster")

		err = client.WaitForCluster(cfg.Workspace, clusterName,
			v1.ClusterPhaseRunning, true, framework.WaitOptions{Timeout: 10 * time.Minute})
		Expect(err).NotTo(HaveOccurred(), "K8s cluster did not reach Running phase")
		GinkgoWriter.Println("K8s cluster is ready")

		By("Creating model registry and waiting for Connected phase")
		GinkgoWriter.Printf("Creating model registry: %s\n", modelRegistryName)

		mr := &v1.ModelRegistry{
			Metadata: &v1.Metadata{
				Name:      modelRegistryName,
				Workspace: cfg.Workspace,
			},
			Spec: &v1.ModelRegistrySpec{
				Type: v1.HuggingFaceModelRegistryType,
				Url:  "https://huggingface.co",
			},
		}

		if cfg.HFToken != "" {
			mr.Spec.Credentials = cfg.HFToken
		}

		_, err = client.CreateModelRegistry(mr)
		Expect(err).NotTo(HaveOccurred(), "Failed to create model registry")

		err = client.WaitForModelRegistry(cfg.Workspace, modelRegistryName,
			v1.ModelRegistryPhaseCONNECTED, framework.WaitOptions{Timeout: 2 * time.Minute})
		Expect(err).NotTo(HaveOccurred(), "Model registry did not reach Connected phase")
		GinkgoWriter.Println("Model registry is ready")

		By("Creating endpoint and waiting for Running phase")
		GinkgoWriter.Printf("Creating endpoint: %s with model %s\n", endpointName, cfg.TestModel)

		cpu := "2"
		memory := "4"
		replicas := 1

		ep := &v1.Endpoint{
			Metadata: &v1.Metadata{
				Name:      endpointName,
				Workspace: cfg.Workspace,
			},
			Spec: &v1.EndpointSpec{
				Cluster: clusterName,
				Model: &v1.ModelSpec{
					Registry: modelRegistryName,
					Name:     cfg.TestModel,
					File:     cfg.TestModelFile,
					Version:  cfg.TestModelVersion,
					Task:     "text-generation",
				},
				Engine: &v1.EndpointEngineSpec{
					Engine:  cfg.TestEngine,
					Version: cfg.EngineVersion,
				},
				Resources: &v1.ResourceSpec{
					CPU:    &cpu,
					Memory: &memory,
				},
				Replicas: v1.ReplicaSpec{
					Num: &replicas,
				},
				DeploymentOptions: map[string]interface{}{
					"scheduler": map[string]interface{}{
						"type": "consistent_hash",
					},
				},
				Variables: map[string]interface{}{
					"engine_args": map[string]interface{}{},
				},
			},
		}

		_, err = client.CreateEndpoint(ep)
		Expect(err).NotTo(HaveOccurred(), "Failed to create endpoint")

		endpoint, err := client.WaitForEndpoint(cfg.Workspace, endpointName,
			v1.EndpointPhaseRUNNING, framework.WaitOptions{Timeout: 15 * time.Minute})
		Expect(err).NotTo(HaveOccurred(), "Endpoint did not reach Running phase")
		Expect(endpoint.Status).NotTo(BeNil(), "Endpoint status is nil")
		Expect(endpoint.Status.ServiceURL).NotTo(BeEmpty(), "Endpoint service URL is empty")

		serviceURL := endpoint.Status.ServiceURL
		GinkgoWriter.Printf("Endpoint is ready at: %s\n", serviceURL)

		By("Creating API key and completing chat request")
		GinkgoWriter.Printf("Creating API key: %s\n", apiKeyName)

		apiKey, err := client.CreateAPIKey(cfg.Workspace, apiKeyName, 1000000)
		Expect(err).NotTo(HaveOccurred(), "Failed to create API key")
		Expect(apiKey).To(HavePrefix("sk_"), "API key should start with sk_")
		GinkgoWriter.Println("API key created successfully")

		GinkgoWriter.Printf("Testing chat completion at: %s\n", serviceURL)

		response, err := client.ChatCompletion(serviceURL, apiKey, cfg.TestModel, "Say hello in one word.")
		Expect(err).NotTo(HaveOccurred(), "Chat completion failed")
		Expect(response).NotTo(BeEmpty(), "Chat response is empty")

		GinkgoWriter.Printf("Chat response: %s\n", response)
		GinkgoWriter.Println("Quick Start K8s E2E test completed successfully!")
	})
})
