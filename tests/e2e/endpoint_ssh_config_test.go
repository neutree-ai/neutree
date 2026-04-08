package e2e

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// rayH is package-level so the All Schema Types Describe can also use it.
var rayH *RayHelper

var _ = Describe("SSH Endpoint Config", Ordered, Label("endpoint", "ssh", "config"), func() {
	var clusterName string

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping SSH endpoint config tests")
		}

		clusterName = setupSSHCluster("e2e-epcfg-ssh-")

		By("Setting up model registry")
		SetupModelRegistry()

		By("Getting cluster dashboard URL")
		c := getClusterFullJSON(clusterName)
		Expect(c.Status.DashboardURL).NotTo(BeEmpty())
		rayH = NewRayHelper(c.Status.DashboardURL)
	})

	AfterAll(func() {
		TeardownModelRegistry()
		teardownCluster(clusterName)
	})

	// --- Config Verification via Ray Dashboard ---

	Describe("Config Verification", Ordered, func() {
		var epName string

		BeforeAll(func() {
			epName = "e2e-ep-ssh-cfg-" + Cfg.RunID

			yamlPath := applyEndpointOnCluster(epName, clusterName, profileEngineVersion())
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should have correct model config in Ray Serve", Label("C2613404"), func() {
			appName := profileWorkspace() + "_" + epName
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())
			Expect(appStatus.DeployedAppConfig.Args).NotTo(BeNil())

			model, ok := appStatus.DeployedAppConfig.Args["model"]
			Expect(ok).To(BeTrue(), "args should have model")

			modelMap, isMap := model.(map[string]interface{})
			Expect(isMap).To(BeTrue())

			nameVal, ok := modelMap["name"].(string)
			Expect(ok).To(BeTrue())
			Expect(nameVal).To(ContainSubstring(profileModelName()),
				"model.name should contain %s", profileModelName())

			taskVal, ok := modelMap["task"].(string)
			Expect(ok).To(BeTrue(), "model should have task field")
			Expect(taskVal).NotTo(BeEmpty(), "model.task should not be empty")
		})

		It("should have correct engine import_path", Label("C2613446"), func() {
			appName := profileWorkspace() + "_" + epName
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())
			Expect(appStatus.DeployedAppConfig.ImportPath).NotTo(BeEmpty(),
				"import_path should not be empty")
			Expect(appStatus.DeployedAppConfig.ImportPath).To(ContainSubstring(profileEngineName()),
				"import_path should contain engine name %s", profileEngineName())
		})

		It("should have correct replica count", Label("C2613402"), func() {
			appName := profileWorkspace() + "_" + epName

			By("Checking application config (args.deployment_options)")
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())

			depOpts, ok := appStatus.DeployedAppConfig.Args["deployment_options"]
			Expect(ok).To(BeTrue(), "args should have deployment_options")

			depOptsMap, isMap := depOpts.(map[string]interface{})
			Expect(isMap).To(BeTrue())

			backend, ok := depOptsMap["backend"]
			Expect(ok).To(BeTrue(), "deployment_options should have backend")

			backendMap, isMap := backend.(map[string]interface{})
			Expect(isMap).To(BeTrue())

			numReplicas, ok := backendMap["num_replicas"]
			Expect(ok).To(BeTrue(), "backend should have num_replicas")

			replicas, isFloat := numReplicas.(float64)
			Expect(isFloat).To(BeTrue())
			Expect(int64(replicas)).To(Equal(int64(1)),
				"args.deployment_options.backend.num_replicas should be 1")

			By("Checking runtime deployment config (deployments.Backend)")
			deps, err := rayH.GetAppRuntimeDeployments(profileWorkspace(), epName)
			Expect(err).NotTo(HaveOccurred())

			backend2, ok := deps["Backend"]
			Expect(ok).To(BeTrue(), "Backend deployment should exist")

			runtimeReplicas, err := backend2.DeploymentConfig.NumReplicas.Float64()
			Expect(err).NotTo(HaveOccurred())
			Expect(int64(runtimeReplicas)).To(Equal(int64(1)),
				"Backend deployment_config.num_replicas should be 1")
		})

		It("should have model cache paths in config", Label("C2613405"), func() {
			appName := profileWorkspace() + "_" + epName
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())

			model, ok := appStatus.DeployedAppConfig.Args["model"]
			Expect(ok).To(BeTrue(), "args should have model")

			modelMap, isMap := model.(map[string]interface{})
			Expect(isMap).To(BeTrue())

			path, ok := modelMap["path"]
			Expect(ok).To(BeTrue(), "model should have path")
			Expect(path).NotTo(BeEmpty())
		})

		It("should have model registry_path in config", Label("C2613406"), func() {
			appName := profileWorkspace() + "_" + epName
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())

			model, ok := appStatus.DeployedAppConfig.Args["model"]
			Expect(ok).To(BeTrue(), "args should have model")

			modelMap, isMap := model.(map[string]interface{})
			Expect(isMap).To(BeTrue())

			regPath, ok := modelMap["registry_path"]
			Expect(ok).To(BeTrue(), "model should have registry_path")
			Expect(regPath).NotTo(BeEmpty())
		})

		It("should have GPU resource config", Label("C2613399"), func() {
			deps, err := rayH.GetAppRuntimeDeployments(profileWorkspace(), epName)
			Expect(err).NotTo(HaveOccurred())

			backend, ok := deps["Backend"]
			Expect(ok).To(BeTrue(), "Backend deployment should exist")
			Expect(backend.DeploymentConfig.RayActorOptions.NumGPUs).To(BeNumerically(">", 0),
				"Backend should have num_gpus > 0")
		})

		It("should have CPU config", Label("C2613388"), func() {
			deps, err := rayH.GetAppRuntimeDeployments(profileWorkspace(), epName)
			Expect(err).NotTo(HaveOccurred())

			controller, ok := deps["Controller"]
			Expect(ok).To(BeTrue(), "Controller deployment should exist")
			Expect(controller.DeploymentConfig.RayActorOptions.NumCPUs).To(BeNumerically(">", 0),
				"Controller should have num_cpus > 0")
		})

		It("should have memory config", Label("C2613397"), func() {
			deps, err := rayH.GetAppRuntimeDeployments(profileWorkspace(), epName)
			Expect(err).NotTo(HaveOccurred())

			backend, ok := deps["Backend"]
			Expect(ok).To(BeTrue(), "Backend deployment should exist")
			Expect(backend.DeploymentConfig.RayActorOptions.Memory).To(BeNumerically(">=", 0),
				"Backend should have memory >= 0")
		})

		It("should have accelerator type in resources", Label("C2613401"), func() {
			deps, err := rayH.GetAppRuntimeDeployments(profileWorkspace(), epName)
			Expect(err).NotTo(HaveOccurred())

			backend, ok := deps["Backend"]
			Expect(ok).To(BeTrue(), "Backend deployment should exist")
			Expect(backend.DeploymentConfig.RayActorOptions.NumGPUs).To(Equal(1.0),
				"Backend should have num_gpus=1 matching endpoint spec")
			Expect(backend.DeploymentConfig.RayActorOptions.Resources).NotTo(BeEmpty(),
				"Backend should have accelerator resources")
		})

		It("should have backend_container for engine isolation", func() {
			appName := profileWorkspace() + "_" + epName
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())
			Expect(appStatus.DeployedAppConfig.Args).To(HaveKey("backend_container"))
		})
	})

	// --- Env Vars Propagation ---

	Describe("Env Vars Propagation", Ordered, Label("env"), func() {
		var envEpName string

		BeforeAll(func() {
			envEpName = "e2e-ep-ssh-env-" + Cfg.RunID
		})

		AfterAll(func() {
			if envEpName != "" {
				deleteEndpoint(envEpName)
			}
		})

		It("should deploy with env vars and reach Running", Label("C2644064"), func() {
			testEnv := map[string]string{
				"E2E_TEST_KEY": "e2e_test_value",
			}

			yamlPath := applyEndpointWithEnv(envEpName, clusterName, profileEngineVersion(), testEnv)
			defer os.Remove(yamlPath)

			waitEndpointRunning(envEpName)

			ep := getEndpoint(envEpName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))

			// Verify inference works with custom env vars
			code, body := inferChat(ep.Status.ServiceURL, "Hello with env vars")
			Expect(code).To(Equal(200), "inference with env vars failed: %s", body)

			By("Verifying env vars propagated to Ray Serve runtime_env")
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			found := false
			for _, appStatus := range apps.Applications {
				if appStatus.DeployedAppConfig == nil {
					continue
				}
				runtimeEnv := appStatus.DeployedAppConfig.RuntimeEnv
				if runtimeEnv == nil {
					continue
				}
				envVars, ok := runtimeEnv["env_vars"]
				if !ok {
					continue
				}
				envMap, ok := envVars.(map[string]interface{})
				if !ok {
					continue
				}
				if val, ok := envMap["E2E_TEST_KEY"]; ok {
					Expect(val).To(Equal("e2e_test_value"),
						"E2E_TEST_KEY should be propagated to runtime_env.env_vars")
					found = true

					break
				}
			}
			Expect(found).To(BeTrue(),
				"should find E2E_TEST_KEY in Ray Serve runtime_env.env_vars")
		})
	})

	// --- Serving-Only Param ---

	Describe("Serving-Only Param", Ordered, Label("serving-param"), func() {
		var servingEpName string

		BeforeAll(func() {
			servingEpName = "e2e-ep-ssh-srvonly-" + Cfg.RunID
		})

		AfterAll(func() {
			if servingEpName != "" {
				deleteEndpoint(servingEpName)
			}
		})

		It("should deploy with serving-only engine_arg ignored", Label("C2644063"), func() {
			accType, accProduct := getClusterAccelerator(clusterName)

			// response_role is a serving-only param in vLLM, should be ignored during engine init.
			// Reaching Running is a sufficient assertion here: vLLM engine startup will fail
			// if an unrecognized non-serving parameter is passed. Serving-only params
			// (response_role, chat_template, etc.) are stripped before engine init, so
			// Running proves the param was correctly classified and ignored.
			servingArgsYAML := engineArgsYAML() + "\n      response_role: assistant"

			defaults := map[string]string{
				"E2E_ENDPOINT_NAME":       servingEpName,
				"E2E_WORKSPACE":           profileWorkspace(),
				"E2E_CLUSTER_NAME":        clusterName,
				"E2E_ENGINE_NAME":         profileEngineName(),
				"E2E_ENGINE_VERSION":      profileEngineVersion(),
				"E2E_MODEL_REGISTRY":      testRegistry(),
				"E2E_MODEL_NAME":          profileModelName(),
				"E2E_MODEL_VERSION":       profileModelVersion(),
				"E2E_MODEL_TASK":          profileModelTask(),
				"E2E_ACCELERATOR_TYPE":    accType,
				"E2E_ACCELERATOR_PRODUCT": accProduct,
				"E2E_ENGINE_ARGS_YAML":    servingArgsYAML,
				"E2E_ENV_YAML":            "",
			}

			yamlPath, err := renderTemplateToTempFile("testdata/endpoint.yaml", defaults)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath, "--force-update")
			ExpectSuccess(r)

			waitEndpointRunning(servingEpName)

			ep := getEndpoint(servingEpName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})
	})

	// --- All Schema Types ---

	Describe("All Schema Types Engine Args", Ordered, Label("schema"), func() {
		var schemaEpName string

		BeforeAll(func() {
			schemaEpName = "e2e-ep-ssh-schema-" + Cfg.RunID
		})

		AfterAll(func() {
			if schemaEpName != "" {
				deleteEndpoint(schemaEpName)
			}
		})

		It("should deploy with all schema data types in engine_args", Label("C2642245", "C2644062"), func() {
			yamlPath := applyEndpointAllSchemaTypes(schemaEpName, clusterName)
			defer os.Remove(yamlPath)

			waitEndpointRunning(schemaEpName)

			ep := getEndpoint(schemaEpName)
			Expect(ep.Status.Phase).To(BeEquivalentTo("Running"))
		})

		It("should have engine_args reflected in Ray Serve config", func() {
			appName := profileWorkspace() + "_" + schemaEpName
			apps, err := rayH.GetServeApplications()
			Expect(err).NotTo(HaveOccurred())

			appStatus, ok := apps.Applications[appName]
			Expect(ok).To(BeTrue(), "application %s should exist", appName)
			Expect(appStatus.DeployedAppConfig).NotTo(BeNil())
			Expect(appStatus.DeployedAppConfig.Args).NotTo(BeNil())

			engineArgs, ok := appStatus.DeployedAppConfig.Args["engine_args"]
			Expect(ok).To(BeTrue(), "args should have engine_args")

			eaMap, isMap := engineArgs.(map[string]interface{})
			Expect(isMap).To(BeTrue(), "engine_args should be a map")

			Expect(eaMap).To(HaveKeyWithValue("dtype", "half"))
			Expect(eaMap).To(HaveKey("seed"))
			Expect(eaMap["seed"]).To(BeNumerically("==", 42))
		})

		It("should serve inference with all-types config", func() {
			ep := getEndpoint(schemaEpName)
			code, body := inferChat(ep.Status.ServiceURL, "Hello with all schema types")
			Expect(code).To(Equal(200), "inference failed: %s", body)
		})
	})
})
