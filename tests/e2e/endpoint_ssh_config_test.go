package e2e

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
)

var _ = Describe("SSH Endpoint Config", Ordered, Label("endpoint", "ssh", "config"), func() {
	var (
		clusterName string
		rayH        *RayHelper
	)

	BeforeAll(func() {
		if profileModelName() == "" {
			Skip("Model name not configured in profile, skipping SSH endpoint config tests")
		}

		requireImageRegistryProfile()

		By("Setting up image registry")
		SetupImageRegistry()

		headIP, workerIPs, sshUser, sshPrivateKey := requireSSHProfile()
		clusterName = "e2e-epcfg-ssh-" + Cfg.RunID

		modelCachePath := profile.ModelCache.HostPath
		if modelCachePath == "" {
			modelCachePath = "/data/models"
		}
		modelCacheYAML := "    model_caches:\n      - name: e2e-cache\n        host_path:\n          path: \"" + modelCachePath + "\"\n"

		yaml := renderSSHClusterYAML(map[string]string{
			"name":              clusterName,
			"head_ip":           headIP,
			"worker_ips":        workerIPs,
			"ssh_user":          sshUser,
			"ssh_private_key":   sshPrivateKey,
			"model_caches_yaml": modelCacheYAML,
		})

		ch := NewClusterHelper()

		By("Applying SSH cluster: " + clusterName)
		r := ch.Apply(yaml)
		ExpectSuccess(r)

		By("Waiting for cluster Running")
		ch.EventuallyInPhase(clusterName, v1.ClusterPhaseRunning, "", TerminalPhaseTimeout)

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
		var (
			epName     string
			appConfig  *dashboard.RayServeApplication
			backendDep RuntimeDeployment
			ctrlDep    RuntimeDeployment
		)

		BeforeAll(func() {
			epName = "e2e-ep-ssh-cfg-" + Cfg.RunID

			yamlPath := applyEndpoint(epName, clusterName, withEnv(map[string]string{
				"E2E_TEST_KEY": "e2e_test_value",
			}))
			defer os.Remove(yamlPath)

			waitEndpointRunning(epName)

			By("Fetching Ray Serve application config")
			appName := profileWorkspace() + "_" + epName
			var err error
			appConfig, err = rayH.GetApplicationConfig(appName)
			Expect(err).NotTo(HaveOccurred())
			Expect(appConfig).NotTo(BeNil(), "application %s should exist", appName)

			By("Fetching runtime deployments")
			deps, err := rayH.GetAppRuntimeDeployments(profileWorkspace(), epName)
			Expect(err).NotTo(HaveOccurred())

			var ok bool
			backendDep, ok = deps["Backend"]
			Expect(ok).To(BeTrue(), "Backend deployment should exist")
			ctrlDep, ok = deps["Controller"]
			Expect(ok).To(BeTrue(), "Controller deployment should exist")
		})

		AfterAll(func() {
			if epName != "" {
				deleteEndpoint(epName)
			}
		})

		It("should have correct model config in Ray Serve", Label("C2613404"), func() {
			model, ok := appConfig.Args["model"].(map[string]any)
			Expect(ok).To(BeTrue(), "args.model should be a map")

			Expect(model["name"]).To(ContainSubstring(profileModelName()),
				"model.name should contain %s", profileModelName())
			Expect(model["task"]).NotTo(BeEmpty(), "model.task should not be empty")
		})

		It("should have correct engine import_path", Label("C2613446"), func() {
			Expect(appConfig.ImportPath).NotTo(BeEmpty())
			Expect(appConfig.ImportPath).To(ContainSubstring(profileEngineName()),
				"import_path should contain engine name %s", profileEngineName())
		})

		It("should have correct replica count", Label("C2613402"), func() {
			By("Checking application config (args.deployment_options.backend.num_replicas)")
			depOpts, ok := appConfig.Args["deployment_options"].(map[string]any)
			Expect(ok).To(BeTrue(), "args should have deployment_options")

			backend, ok := depOpts["backend"].(map[string]any)
			Expect(ok).To(BeTrue(), "deployment_options should have backend")

			Expect(backend["num_replicas"]).To(BeNumerically("==", 1),
				"args.deployment_options.backend.num_replicas should be 1")

			By("Checking runtime deployment config")
			runtimeReplicas, err := backendDep.DeploymentConfig.NumReplicas.Float64()
			Expect(err).NotTo(HaveOccurred())
			Expect(int64(runtimeReplicas)).To(Equal(int64(1)),
				"Backend deployment_config.num_replicas should be 1")
		})

		It("should have model cache paths in config", Label("C2613405"), func() {
			model, ok := appConfig.Args["model"].(map[string]any)
			Expect(ok).To(BeTrue(), "args.model should be a map")

			Expect(model).To(HaveKey("path"), "model should have path")
			Expect(model["path"]).NotTo(BeEmpty())
		})

		It("should have model registry_path in config", Label("C2613406"), func() {
			model, ok := appConfig.Args["model"].(map[string]any)
			Expect(ok).To(BeTrue(), "args.model should be a map")

			Expect(model).To(HaveKey("registry_path"), "model should have registry_path")
			Expect(model["registry_path"]).NotTo(BeEmpty())
		})

		It("should have GPU resource config", Label("C2613399"), func() {
			if backendDep.DeploymentConfig.RayActorOptions.NumGPUs == 0 {
				Skip("No GPU configured on this cluster")
			}
			Expect(backendDep.DeploymentConfig.RayActorOptions.NumGPUs).To(BeNumerically(">", 0),
				"Backend should have num_gpus > 0")
		})

		It("should have CPU config", Label("C2613388"), func() {
			Expect(ctrlDep.DeploymentConfig.RayActorOptions.NumCPUs).To(BeNumerically(">", 0),
				"Controller should have num_cpus > 0")
		})

		It("should have memory config", Label("C2613397"), func() {
			Expect(backendDep.DeploymentConfig.RayActorOptions.Memory).To(BeNumerically(">=", 0),
				"Backend should have memory >= 0")
		})

		It("should have accelerator type in resources", Label("C2613401"), func() {
			if backendDep.DeploymentConfig.RayActorOptions.NumGPUs == 0 {
				Skip("No GPU configured on this cluster")
			}
			Expect(backendDep.DeploymentConfig.RayActorOptions.NumGPUs).To(Equal(1.0),
				"Backend should have num_gpus=1 matching endpoint spec")
			Expect(backendDep.DeploymentConfig.RayActorOptions.Resources).NotTo(BeEmpty(),
				"Backend should have accelerator resources")
		})

		It("should have backend_container for engine isolation", func() {
			Expect(appConfig.Args).To(HaveKey("backend_container"))
		})

		It("should have env vars propagated to Ray Serve runtime_env", Label("C2644064"), func() {
			Expect(appConfig.RuntimeEnv).NotTo(BeNil(), "runtime_env should not be nil")

			envVars, ok := appConfig.RuntimeEnv["env_vars"].(map[string]any)
			Expect(ok).To(BeTrue(), "runtime_env should have env_vars map")

			Expect(envVars).To(HaveKeyWithValue("E2E_TEST_KEY", "e2e_test_value"),
				"E2E_TEST_KEY should be propagated to runtime_env.env_vars")
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
			servingArgs := engineArgsYAML() + "\n      response_role: assistant"

			yamlPath := applyEndpoint(servingEpName, clusterName,
				withEngineArgs(servingArgs))
			defer os.Remove(yamlPath)

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
			yamlPath := applyEndpoint(schemaEpName, clusterName, withEngineArgs(allSchemaTypesEngineArgsYAML()))
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
			code, body, err := inferChat(ep.Status.ServiceURL, "Hello with all schema types")
			Expect(err).NotTo(HaveOccurred())
			Expect(code).To(Equal(200), "inference failed: %s", body)
		})
	})
})
