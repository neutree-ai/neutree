package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
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

		requireImageRegistryProfile()

		By("Setting up image registry")
		SetupImageRegistry()

		kubeconfig := requireK8sProfile()
		clusterName = "e2e-epcfg-k8s-" + Cfg.RunID

		modelCachePath := profile.ModelCache.HostPath
		if modelCachePath == "" {
			modelCachePath = "/data/models"
		}

		yaml := renderK8sClusterYAML(map[string]any{
			"name":       clusterName,
			"kubeconfig": kubeconfig,
			"model_caches": []ModelCache{
				{Name: "e2e-cache", Mode: "host_path", HostPath: modelCachePath},
			},
		})

		ch := NewClusterHelper()

		By("Applying K8s cluster with model cache: " + clusterName)
		r := ch.Apply(yaml)
		ExpectSuccess(r)

		By("Waiting for cluster Running")
		ch.EventuallyInPhase(clusterName, v1.ClusterPhaseRunning, "", TerminalPhaseTimeout)

		By("Setting up K8s helper")
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
		var (
			epName  string
			cluster v1.Cluster
			ep      v1.Endpoint
		)

		BeforeAll(func() {
			epName = "e2e-ep-k8s-cfg-" + Cfg.RunID

			yamlPath := applyEndpoint(epName, clusterName,
				withCPU("4"), withMemory("8"),
				withEnv(map[string]string{
					"E2E_TEST_KEY": "e2e_test_value",
				}))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			cluster = getClusterFullJSON(clusterName)
			ep = getEndpoint(epName)
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should have correct container image from engine", Label("C2613474"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == profileEngineName() {
					Expect(c.Image).To(ContainSubstring(profileEngineVersion()),
						"engine container image should contain version %s", profileEngineVersion())
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find engine container")
		})

		It("should have correct replica count", Label("C2613475"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			Expect(*d.Spec.Replicas).To(Equal(int32(1)))
		})

		It("should have CPU resource requests", Label("C2613468"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				cpu := c.Resources.Requests.Cpu()
				if !cpu.IsZero() {
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find container with non-zero CPU request")
		})

		It("should have memory resource requests", Label("C2613469"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				mem := c.Resources.Requests.Memory()
				if !mem.IsZero() {
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find container with non-zero memory request")
		})

		It("should have detailed engine_args in container args", Label("C2613477"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

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

			Expect(found).To(BeTrue(), "should find engine container %s in endpoint deployment", profileEngineName())
		})

		It("should have custom env vars on container", Label("C2613478"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name != profileEngineName() {
					continue
				}

				envMap := map[string]any{}
				for _, e := range c.Env {
					envMap[e.Name] = e.Value
				}

				Expect(envMap).To(HaveKeyWithValue("E2E_TEST_KEY", "e2e_test_value"),
					"container should have custom env var E2E_TEST_KEY")
				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find engine container in endpoint deployment %s", epName)
		})

		It("should have model-downloader initContainer with download path", Label("C2613481"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, ic := range d.Spec.Template.Spec.InitContainers {
				if ic.Name == "model-downloader" {
					argsStr := strings.Join(ic.Args, " ")
					Expect(argsStr).To(ContainSubstring("--path"),
						"model-downloader initContainer should have --path arg")
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find model-downloader initContainer in endpoint deployment")
		})

		It("should have imagePullSecrets configured", Label("C2613487"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			Expect(d.Spec.Template.Spec.ImagePullSecrets).NotTo(BeEmpty(),
				"deployment should have imagePullSecrets")

			secretName := d.Spec.Template.Spec.ImagePullSecrets[0].Name
			Expect(secretName).NotTo(BeEmpty(), "imagePullSecret name should not be empty")
		})

		It("should have correct image repo from registry config", Label("C2613488"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			Expect(d.Spec.Template.Spec.Containers).NotTo(BeEmpty())

			image := d.Spec.Template.Spec.Containers[0].Image
			if profile.ImageRegistry.Repository != "" {
				Expect(image).To(ContainSubstring(profile.ImageRegistry.Repository))
			}
		})

		It("should have GPU resource requests when accelerator is GPU", Label("C2613470"), func() {
			accType := ep.Spec.Resources.GetAcceleratorType()

			if accType != "nvidia_gpu" && accType != "amd_gpu" {
				Skip("Endpoint accelerator is not GPU, type=" + accType)
			}

			var expectedResource string
			switch accType {
			case "nvidia_gpu":
				expectedResource = "nvidia.com/gpu"
			case "amd_gpu":
				expectedResource = "amd.com/gpu"
			}

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				if qty, ok := c.Resources.Requests[corev1.ResourceName(expectedResource)]; ok {
					Expect(qty.Value()).To(BeNumerically(">=", 1),
						"GPU resource %s should be >= 1", expectedResource)
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(),
				"should find container with GPU resource %s in requests", expectedResource)
		})

		It("should have GPU product nodeSelector when configured", Label("C2613471"), func() {
			accType := ep.Spec.Resources.GetAcceleratorType()
			accProduct := ep.Spec.Resources.GetAcceleratorProduct()

			if accProduct == "" {
				Skip("Cluster has no accelerator product info")
			}

			var expectedKey string
			switch accType {
			case "nvidia_gpu":
				expectedKey = "nvidia.com/gpu.product"
			case "amd_gpu":
				expectedKey = "amd.com/gpu.product-name"
			default:
				Skip("Unknown accelerator type for nodeSelector: " + accType)
			}

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			Expect(d.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue(expectedKey, accProduct),
				"nodeSelector should have %s=%s", expectedKey, accProduct)
		})

		It("should have model cache volume mounted", Label("C2613482"), func() {
			modelCaches := cluster.Spec.Config.ModelCaches
			Expect(modelCaches).NotTo(BeEmpty(), "cluster should have model_caches configured")

			cache := modelCaches[0]
			expectedVolName := "models-cache"
			if cache.Name != "" {
				expectedVolName = "models-cache-" + cache.Name
			}
			expectedMountPath := "/models-cache"
			if cache.Name != "" {
				expectedMountPath = "/models-cache/" + cache.Name
			}

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			By("Checking volume exists: " + expectedVolName)
			volFound := false
			for _, vol := range d.Spec.Template.Spec.Volumes {
				if vol.Name == expectedVolName {
					volFound = true

					break
				}
			}
			Expect(volFound).To(BeTrue(),
				"should find volume %s in deployment", expectedVolName)

			By("Checking volumeMount exists: " + expectedMountPath)
			mountFound := false
			for _, c := range d.Spec.Template.Spec.Containers {
				for _, vm := range c.VolumeMounts {
					if vm.Name == expectedVolName && vm.MountPath == expectedMountPath {
						mountFound = true

						break
					}
				}
			}
			if !mountFound {
				for _, ic := range d.Spec.Template.Spec.InitContainers {
					for _, vm := range ic.VolumeMounts {
						if vm.Name == expectedVolName && vm.MountPath == expectedMountPath {
							mountFound = true

							break
						}
					}
				}
			}
			Expect(mountFound).To(BeTrue(),
				"should find volumeMount %s at %s", expectedVolName, expectedMountPath)
		})

		It("should confirm model downloaded via Running status", Label("C2613483"), func() {
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"),
				"Running confirms model was downloaded from cache")
		})

		It("should have correct model args in container", Label("C2613473"), func() {
			modelCaches := cluster.Spec.Config.ModelCaches
			cacheName := "default"
			if len(modelCaches) > 0 && modelCaches[0].Name != "" {
				cacheName = modelCaches[0].Name
			}
			expectedModelPath := "/models-cache/" + cacheName + "/" + profileModelName()

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, container := range d.Spec.Template.Spec.Containers {
				if container.Name != profileEngineName() {
					continue
				}

				allArgs := append(container.Command, container.Args...)
				argsStr := strings.Join(allArgs, " ")
				Expect(argsStr).To(ContainSubstring(expectedModelPath),
					"engine container args should contain model path %s", expectedModelPath)
				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find engine container with model args")
		})

		It("should have model cache registry_path in initContainer args", Label("C2613480"), func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, epName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", epName)

			found := false

			for _, ic := range d.Spec.Template.Spec.InitContainers {
				if ic.Name == "model-downloader" {
					argsStr := strings.Join(ic.Args, " ")
					Expect(argsStr).To(ContainSubstring("--registry_path"),
						"model-downloader args should contain --registry_path flag")
					Expect(argsStr).To(ContainSubstring(profileModelName()),
						"model-downloader --registry_path should contain model name %s", profileModelName())
					found = true

					break
				}
			}

			Expect(found).To(BeTrue(), "should find model-downloader initContainer")
		})
	})

	Describe("vLLM Task Translation", Ordered, Label("config", "task-translation", "vllm"), func() {
		type taskCase struct {
			suffix      string
			task        string
			wantRunner  string
			wantConvert string
		}

		DescribeTable("should translate model task into runner and convert flags",
			func(tc taskCase) {
				if profileEngineName() != v1.EngineNameVLLM {
					Skip("vLLM task translation is only applicable to the vLLM engine")
				}

				epName := "e2e-ep-k8s-task-" + tc.suffix + "-" + Cfg.RunID
				yamlPath := applyEndpoint(epName, clusterName,
					withTask(tc.task),
					withEngineArgs([]EngineArg{{Key: "dtype", Value: "half"}}))
				DeferCleanup(func() { deleteEndpoint(epName) })
				DeferCleanup(os.Remove, yamlPath)

				cmd := eventuallyEndpointContainerCommand(k8sH, namespace, epName, profileEngineName())
				Expect(commandFlagValue(cmd, "--runner")).To(Equal(tc.wantRunner),
					"--runner mismatch for task %s (command=%v)", tc.task, cmd)
				Expect(commandFlagValue(cmd, "--convert")).To(Equal(tc.wantConvert),
					"--convert mismatch for task %s (command=%v)", tc.task, cmd)
			},
			Entry("text-embedding uses pooling/embed", taskCase{
				suffix:      "embed",
				task:        "text-embedding",
				wantRunner:  "pooling",
				wantConvert: "embed",
			}),
			Entry("text-rerank uses pooling/classify", taskCase{
				suffix:      "rerank",
				task:        "text-rerank",
				wantRunner:  "pooling",
				wantConvert: "classify",
			}),
			Entry("text-generation keeps vLLM auto defaults", taskCase{
				suffix:      "gen",
				task:        "text-generation",
				wantRunner:  "",
				wantConvert: "",
			}),
		)
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
			yamlPath := applyEndpoint(schemaEpName, clusterName, withEngineArgs(allSchemaTypesEngineArgs()))
			defer os.Remove(yamlPath)

			waitEndpointRunning(schemaEpName)

			ep := getEndpoint(schemaEpName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should have all engine_args as container CLI flags", func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, schemaEpName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", schemaEpName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name != profileEngineName() {
					continue
				}

				allArgs := append(c.Command, c.Args...)
				argsStr := strings.Join(allArgs, " ")

				Expect(argsStr).To(ContainSubstring("--dtype"), "string enum")
				Expect(argsStr).To(ContainSubstring("--max_model_len"), "integer")
				Expect(argsStr).To(ContainSubstring("--gpu_memory_utilization"), "number/float")
				Expect(argsStr).To(ContainSubstring("--enforce_eager"), "boolean true (flag-only)")
				Expect(argsStr).To(ContainSubstring("--seed"), "integer")
				Expect(argsStr).To(ContainSubstring("--override_generation_config"), "object/JSON")

				// Boolean false engine_args must drop the flag entirely. vLLM
				// argparse rejects `--flag false` (store_true is nargs=0), so
				// the template skips both `--<flag>` and `"false"` when the
				// value is the literal string "false".
				Expect(argsStr).NotTo(ContainSubstring("--enable_prefix_caching"),
					"boolean false engine_arg must not emit its flag")
				for _, tok := range allArgs {
					Expect(tok).NotTo(Equal("false"),
						"no CLI token should be the literal string \"false\"")
				}

				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find engine container in schema endpoint deployment")
		})

		It("should serve inference with all-types config", func() {
			ep := getEndpoint(schemaEpName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello schema types")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(200), "inference failed: %s", body)
		})
	})

	// --- SGLang All Schema Types ---
	//
	// Mirrors the vLLM "All Schema Types Engine Args" block above: deploy with
	// a multi-type engine_args YAML, assert each value reaches the engine as
	// a container CLI flag (kebab-case via the K8s template's `_` → `-`
	// conversion), then exercise inference end-to-end.

	Describe("SGLang All Schema Types Engine Args", Ordered, Label("config", "schema", "sglang"), func() {
		var schemaEpName string

		BeforeAll(func() {
			schemaEpName = "e2e-ep-k8s-sglang-schema-" + Cfg.RunID
		})

		AfterAll(func() {
			if schemaEpName != "" {
				deleteEndpoint(schemaEpName)
			}
		})

		It("should deploy SGLang with all schema data types", Label("C2649562"), func() {
			yamlPath := applyEndpoint(schemaEpName, clusterName,
				withEngine("sglang", profileEngineVersionFor("sglang")),
				withEngineArgs(allSchemaTypesEngineArgsSGLang()))
			defer os.Remove(yamlPath)

			waitEndpointRunning(schemaEpName)

			ep := getEndpoint(schemaEpName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should have all engine_args as container CLI flags", func() {
			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, schemaEpName)
			Expect(err).NotTo(HaveOccurred(), "should find endpoint deployment %s", schemaEpName)

			found := false

			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name != v1.EngineNameSGLang {
					continue
				}

				allArgs := append(c.Command, c.Args...)
				argsStr := strings.Join(allArgs, " ")

				// SGLang's K8s template converts underscore engine_args keys to
				// kebab-case CLI flags (sprig replace "_" "-"); assert kebab.
				Expect(argsStr).To(ContainSubstring("--tp-size"), "integer")
				Expect(argsStr).To(ContainSubstring("--mem-fraction-static"), "number/float")
				Expect(argsStr).To(ContainSubstring("--disable-cuda-graph"), "boolean true (flag-only)")
				Expect(argsStr).To(ContainSubstring("--dtype"), "string enum")
				Expect(argsStr).To(ContainSubstring("--chunked-prefill-size"), "integer")
				Expect(argsStr).To(ContainSubstring("--served-model-name"), "string")
				Expect(argsStr).To(ContainSubstring("--attention-backend"), "string enum")
				Expect(argsStr).To(ContainSubstring("--cuda-graph-max-bs"), "integer")
				Expect(argsStr).To(ContainSubstring("--preferred-sampling-params"), "object/JSON")
				Expect(argsStr).To(ContainSubstring("--json-model-override-args"), "object/JSON")

				// Boolean false engine_args must drop the flag entirely. SGLang
				// argparse rejects `--flag false` (store_true is nargs=0), so
				// the template skips both `--<flag>` and `"false"` when the
				// value is the literal string "false". Check both kebab and
				// underscore forms because the SGLang template applies sprig
				// `replace "_" "-"` only on the emit branches.
				Expect(argsStr).NotTo(ContainSubstring("--skip-tokenizer-init"),
					"boolean false engine_arg must not emit its flag (kebab form)")
				Expect(argsStr).NotTo(ContainSubstring("--skip_tokenizer_init"),
					"boolean false engine_arg must not emit its flag (underscore form)")
				for _, tok := range allArgs {
					Expect(tok).NotTo(Equal("false"),
						"no CLI token should be the literal string \"false\"")
				}

				found = true

				break
			}

			Expect(found).To(BeTrue(), "should find sglang container in schema endpoint deployment")
		})

		It("should serve inference with all-types config", func() {
			ep := getEndpoint(schemaEpName)
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello SGLang schema types")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(200), "inference failed: %s", body)
		})
	})
})

func eventuallyEndpointContainerCommand(k8sH *K8sHelper, namespace, epName, containerName string) []string {
	var cmd []string
	Eventually(func() error {
		var err error
		cmd, err = endpointContainerCommand(context.Background(), k8sH, namespace, epName, containerName)
		return err
	}, TerminalPhaseTimeout).Should(Succeed(), "should find container %s in endpoint deployment %s", containerName, epName)

	return cmd
}

func endpointContainerCommand(ctx context.Context, k8sH *K8sHelper, namespace, epName, containerName string) ([]string, error) {
	d, err := k8sH.GetDeployment(ctx, namespace, epName)
	if err != nil {
		return nil, err
	}

	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			cmd := append([]string{}, c.Command...)
			cmd = append(cmd, c.Args...)
			return cmd, nil
		}
	}

	return nil, fmt.Errorf("container %q not found in deployment %s/%s", containerName, namespace, epName)
}

func commandFlagValue(cmd []string, flag string) string {
	for i, tok := range cmd {
		if tok == flag && i+1 < len(cmd) {
			return strings.Trim(cmd[i+1], `"`)
		}
	}

	return ""
}
