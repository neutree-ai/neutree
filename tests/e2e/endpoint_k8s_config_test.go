package e2e

import (
	"context"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("K8s Endpoint Config", Ordered, Label("endpoint", "k8s", "config"), func() {
	var (
		clusterName string
		k8sH        *K8sHelper
		namespace   string
	)

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping K8s endpoint config tests")
		}

		clusterName = setupK8sCluster("e2e-epcfg-k8s-")

		By("Setting up K8s helper")
		kubeconfig := requireK8sProfile()
		k8sH = NewK8sHelper(kubeconfig)

		By("Resolving namespace")
		c := getClusterFullJSON(clusterName)
		namespace = ClusterNamespace(c.Metadata.Workspace, c.Metadata.Name, c.ID)

		By("Setting up model registry")
		SetupModelRegistry()
	})

	AfterAll(func() {
		TeardownModelRegistry()
		teardownCluster(clusterName)
	})

	Describe("Config Verification", Ordered, Label("config"), func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-k8s-cfg-" + Cfg.RunID

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should have correct container image from engine", Label("C2613474"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					if c.Name == profileEngineName() {
						Expect(c.Image).To(ContainSubstring(profileEngineVersion()),
							"engine container image should contain version %s", profileEngineVersion())
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(), "should find engine container")
		})

		It("should have correct replica count", Label("C2613475"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				Expect(*d.Spec.Replicas).To(Equal(int32(1)))
				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find endpoint deployment %s", epName)
		})

		It("should have CPU resource requests", Label("C2613468"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					cpu := c.Resources.Requests.Cpu()
					if !cpu.IsZero() {
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(), "should find container with non-zero CPU request")
		})

		It("should have memory resource requests", Label("C2613469"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					mem := c.Resources.Requests.Memory()
					if !mem.IsZero() {
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(), "should find container with non-zero memory request")
		})

		It("should have detailed engine_args in container args", Label("C2613477"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					if c.Name != profileEngineName() {
						continue
					}

					allArgs := append(c.Command, c.Args...)
					argsStr := strings.Join(allArgs, " ")
					Expect(argsStr).To(ContainSubstring("--dtype"),
						"container command/args should contain --dtype from engine_args")
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find engine container %s in endpoint deployment", profileEngineName())
		})

		It("should have custom env vars on container", Label("C2613478"), func() {
			envEpName := "e2e-ep-env-" + Cfg.RunID
			testEnv := map[string]string{
				"E2E_TEST_KEY": "e2e_test_value",
			}

			yamlPath := applyEndpointWithEnv(envEpName, clusterName, profileEngineVersion(), testEnv)
			defer os.Remove(yamlPath)
			defer deleteEndpoint(envEpName)

			waitEndpointRunning(envEpName)

			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, envEpName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					if c.Name != profileEngineName() {
						continue
					}

					envMap := map[string]string{}
					for _, e := range c.Env {
						envMap[e.Name] = e.Value
					}

					Expect(envMap).To(HaveKeyWithValue("E2E_TEST_KEY", "e2e_test_value"),
						"container should have custom env var E2E_TEST_KEY")
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find engine container in endpoint deployment %s", envEpName)
		})

		It("should have model-downloader initContainer with download path", Label("C2613481"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, ic := range d.Spec.Template.Spec.InitContainers {
					if ic.Name == "model-downloader" {
						argsStr := strings.Join(ic.Args, " ")
						Expect(argsStr).To(ContainSubstring("--path"),
							"model-downloader initContainer should have --path arg")
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(), "should find model-downloader initContainer in endpoint deployment")
		})

		It("should have imagePullSecrets configured", Label("C2613487"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				Expect(d.Spec.Template.Spec.ImagePullSecrets).NotTo(BeEmpty(),
					"deployment should have imagePullSecrets")

				secretName := d.Spec.Template.Spec.ImagePullSecrets[0].Name
				Expect(secretName).NotTo(BeEmpty(), "imagePullSecret name should not be empty")
				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find endpoint deployment")
		})

		It("should have correct image repo from registry config", Label("C2613488"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				Expect(d.Spec.Template.Spec.Containers).NotTo(BeEmpty())

				image := d.Spec.Template.Spec.Containers[0].Image
				if profile.ImageRegistry.Repository != "" {
					Expect(image).To(ContainSubstring(profile.ImageRegistry.Repository))
				}

				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find endpoint deployment %s", epName)
		})

		It("should have GPU resource requests when accelerator is GPU", Label("C2613470"), func() {
			c := getClusterFullJSON(clusterName)
			accType := clusterAcceleratorType(c)

			if accType != "nvidia_gpu" && accType != "amd_gpu" {
				Skip("Accelerator is not GPU, type=" + accType)
			}

			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					for resName, qty := range c.Resources.Requests {
						if strings.Contains(string(resName), "gpu") {
							Expect(qty.Value()).To(BeNumerically(">=", 1),
								"GPU resource %s should be >= 1", resName)
							found = true

							break
						}
					}
				}
			}

			Expect(found).To(BeTrue(), "should find container with GPU resource request")
		})

		It("should have GPU product nodeSelector when configured", Label("C2613471"), func() {
			c := getClusterFullJSON(clusterName)
			accProduct := clusterAcceleratorProduct(c)

			if accProduct == "" {
				Skip("Cluster has no accelerator product info")
			}

			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for key, val := range d.Spec.Template.Spec.NodeSelector {
					if strings.Contains(key, "gpu.product") || strings.Contains(key, "product-name") {
						Expect(val).To(Equal(accProduct))
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(),
				"should find nodeSelector with gpu product %s", accProduct)
		})

		It("should have model cache volume mounted", Label("C2613482"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, vol := range d.Spec.Template.Spec.Volumes {
					if strings.Contains(vol.Name, "model") || strings.Contains(vol.Name, "cache") {
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(), "should find model cache volume in endpoint deployment")
		})

		It("should confirm model downloaded via Running status", Label("C2613483"), func() {
			ep := getEndpoint(epName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
				"Running confirms model was downloaded from cache")
		})

		It("should have correct model args in container", Label("C2613473"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					if c.Name != profileEngineName() {
						continue
					}

					allArgs := append(c.Command, c.Args...)
					argsStr := strings.Join(allArgs, " ")
					Expect(argsStr).To(ContainSubstring("--model"),
						"engine container should have --model arg")
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find engine container with model args")
		})

		It("should have model cache registry_path in initContainer args", Label("C2613480"), func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, epName) {
					continue
				}

				for _, ic := range d.Spec.Template.Spec.InitContainers {
					if ic.Name == "model-downloader" {
						argsStr := strings.Join(ic.Args, " ")
						Expect(argsStr).To(ContainSubstring(profileModelName()),
							"model-downloader args should contain model name %s", profileModelName())
						found = true

						break
					}
				}
			}

			Expect(found).To(BeTrue(), "should find model-downloader initContainer")
		})
	})

	// --- All Schema Types ---

	Describe("All Schema Types Engine Args", Ordered, Label("config", "schema"), func() {
		var schemaEpName string

		BeforeAll(func() {
			schemaEpName = "e2e-ep-k8s-schema-" + Cfg.RunID
		})

		AfterAll(func() {
			if schemaEpName != "" {
				deleteEndpoint(schemaEpName)
			}
		})

		It("should deploy with all schema data types", Label("C2642246"), func() {
			yamlPath := applyEndpointAllSchemaTypes(schemaEpName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(schemaEpName)

			ep := getEndpoint(schemaEpName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should have all engine_args as container CLI flags", func() {
			ctx := context.Background()
			deploys, err := k8sH.ListDeployments(ctx, namespace, "app=inference")
			Expect(err).NotTo(HaveOccurred())

			found := false

			for _, d := range deploys {
				if !strings.Contains(d.Name, schemaEpName) {
					continue
				}

				for _, c := range d.Spec.Template.Spec.Containers {
					if c.Name != profileEngineName() {
						continue
					}

					allArgs := append(c.Command, c.Args...)
					argsStr := strings.Join(allArgs, " ")

					Expect(argsStr).To(ContainSubstring("--dtype"), "string enum")
					Expect(argsStr).To(ContainSubstring("--max_model_len"), "integer")
					Expect(argsStr).To(ContainSubstring("--gpu_memory_utilization"), "number/float")
					Expect(argsStr).To(ContainSubstring("--enforce_eager"), "boolean")
					Expect(argsStr).To(ContainSubstring("--override_generation_config"), "object/JSON")
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find engine container in schema endpoint deployment")
		})

		It("should serve inference with all-types config", func() {
			ep := getEndpoint(schemaEpName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello schema types")
			Expect(code).To(Equal(200), "inference failed: %s", body)
		})
	})
})
