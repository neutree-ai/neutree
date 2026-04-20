package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("K8s Cluster Config", Ordered, Label("cluster", "k8s", "config"), func() {
	var ClusterH *ClusterHelper

	BeforeAll(func() {
		requireImageRegistryEnv()

		By("Setting up image registry")
		SetupImageRegistry()
		ClusterH = NewClusterHelper()
	})

	AfterAll(func() {
		TeardownImageRegistry()
	})

	// --- CR Verification ---

	Describe("CR Verification", Ordered, func() {
		var (
			clusterName string
			kubeconfig  string
			k8sH        *K8sHelper
			namespace   string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			clusterName = "e2e-k8s-verify-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			k8sH = NewK8sHelper(kubeconfig)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			namespace = ClusterNamespace(c.Metadata.Workspace, c.Metadata.Name, c.ID)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should create namespace for cluster", Label("C2612761"), func() {
			ctx := context.Background()
			ns, err := k8sH.GetNamespace(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(ns.Name).To(HavePrefix("neutree-cluster-"))
		})

		It("should have neutree labels on all CR objects", Label("C2612764"), func() {
			ctx := context.Background()

			By("Checking namespace labels")
			ns, err := k8sH.GetNamespace(ctx, namespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(ns.Labels).To(HaveKeyWithValue("neutree.ai/neutree-cluster", clusterName))
			Expect(ns.Labels).To(HaveKeyWithValue("neutree.ai/neutree-workspace", profileWorkspace()))

			managedBy := HaveKeyWithValue("app.kubernetes.io/managed-by", "neutree.ai")
			clusterLabel := HaveKeyWithValue("neutree.ai/neutree-cluster", clusterName)

			By("Checking image-pull-secret labels")
			secret, err := k8sH.GetSecret(ctx, namespace, "image-pull-secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(secret.Labels).To(managedBy)
			Expect(secret.Labels).To(clusterLabel)

			By("Checking vmagent deployment labels")
			vmagent, err := k8sH.GetDeployment(ctx, namespace, "vmagent")
			Expect(err).NotTo(HaveOccurred())
			Expect(vmagent.Labels).To(managedBy)

			By("Checking router deployment labels")
			router, err := k8sH.GetDeployment(ctx, namespace, "router")
			Expect(err).NotTo(HaveOccurred())
			Expect(router.Labels).To(managedBy)

			By("Checking router-service labels")
			routerSvc, err := k8sH.GetService(ctx, namespace, "router-service")
			Expect(err).NotTo(HaveOccurred())
			Expect(routerSvc.Labels).To(managedBy)

			By("Checking vmagent-config ConfigMap labels")
			vmagentCM, err := k8sH.GetConfigMap(ctx, namespace, "vmagent-config")
			Expect(err).NotTo(HaveOccurred())
			Expect(vmagentCM.Labels).To(managedBy)

			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)

			By("Checking metrics deploy config CM labels")
			metricsCM, err := k8sH.GetConfigMap(ctx, namespace, fmt.Sprintf("neutree-%s-metrics-config", c.Metadata.Name))
			Expect(err).NotTo(HaveOccurred())
			Expect(metricsCM.Labels).To(managedBy)

			By("Checking router deploy config CM labels")
			routerCM, err := k8sH.GetConfigMap(ctx, namespace, fmt.Sprintf("neutree-%s-router-config", c.Metadata.Name))
			Expect(err).NotTo(HaveOccurred())
			Expect(routerCM.Labels).To(managedBy)
		})

		It("should create imagePullSecret", Label("C2612762"), func() {
			ctx := context.Background()
			s, err := k8sH.GetSecret(ctx, namespace, "image-pull-secret")
			Expect(err).NotTo(HaveOccurred(), "image-pull-secret should exist")
			Expect(s.Type).To(Equal(corev1.SecretTypeDockerConfigJson))
		})

		It("should create vmagent observability resources (Deployment, ConfigMap, SA, Role, RoleBinding)", Label("C2612763"), func() {
			ctx := context.Background()

			_, err := k8sH.GetDeployment(ctx, namespace, "vmagent")
			Expect(err).NotTo(HaveOccurred(), "vmagent deployment should exist")

			_, err = k8sH.GetConfigMap(ctx, namespace, "vmagent-config")
			Expect(err).NotTo(HaveOccurred(), "vmagent-config ConfigMap should exist")

			_, err = k8sH.GetServiceAccount(ctx, namespace, "vmagent-service-account")
			Expect(err).NotTo(HaveOccurred(), "vmagent ServiceAccount should exist")

			_, err = k8sH.GetRole(ctx, namespace, "vmagent-pod-reader")
			Expect(err).NotTo(HaveOccurred(), "vmagent Role should exist")

			_, err = k8sH.GetRoleBinding(ctx, namespace, "vmagent-rolebinding")
			Expect(err).NotTo(HaveOccurred(), "vmagent RoleBinding should exist")
		})

		It("should create router resources (SA, Role, RoleBinding, Deployment, Service)", Label("C2612779"), func() {
			ctx := context.Background()

			_, err := k8sH.GetServiceAccount(ctx, namespace, "router-service-account")
			Expect(err).NotTo(HaveOccurred(), "router ServiceAccount should exist")

			_, err = k8sH.GetRole(ctx, namespace, "router-pod-reader")
			Expect(err).NotTo(HaveOccurred(), "router Role should exist")

			_, err = k8sH.GetRoleBinding(ctx, namespace, "router-rolebinding")
			Expect(err).NotTo(HaveOccurred(), "router RoleBinding should exist")

			_, err = k8sH.GetDeployment(ctx, namespace, "router")
			Expect(err).NotTo(HaveOccurred(), "router Deployment should exist")

			_, err = k8sH.GetService(ctx, namespace, "router-service")
			Expect(err).NotTo(HaveOccurred(), "router Service should exist")
		})

		It("should create deploy config CM for metrics component", Label("C2623075"), func() {
			ctx := context.Background()

			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			cmName := fmt.Sprintf("neutree-%s-metrics-config", c.Metadata.Name)

			_, err := k8sH.GetConfigMap(ctx, namespace, cmName)
			Expect(err).NotTo(HaveOccurred(), "deploy config CM %s should exist", cmName)
		})

		It("should create deploy config CM for router component", Label("C2623076"), func() {
			ctx := context.Background()

			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			cmName := fmt.Sprintf("neutree-%s-router-config", c.Metadata.Name)

			_, err := k8sH.GetConfigMap(ctx, namespace, cmName)
			Expect(err).NotTo(HaveOccurred(), "deploy config CM %s should exist", cmName)
		})

		It("should clean up namespace after deletion", Label("C2612851"), func() {
			ctx := context.Background()
			Expect(k8sH.NamespaceExists(ctx, namespace)).To(BeTrue())

			r := ClusterH.DeleteGraceful(clusterName)
			ExpectSuccess(r)

			r = ClusterH.WaitForDelete(clusterName, TerminalPhaseTimeout)
			ExpectSuccess(r)

			k8sH.WaitForNamespaceDeleted(ctx, namespace, 2*time.Minute)
		})
	})

	// --- Multi-Cluster Namespace Isolation ---

	Describe("Multi-Cluster Isolation", Label("isolation"), func() {

		It("should create different namespaces for two clusters on same K8s", Label("C2614157"), func() {
			kubeconfig := requireK8sEnv()

			clusterA := "e2e-k8s-iso-a-" + Cfg.RunID
			clusterB := "e2e-k8s-iso-b-" + Cfg.RunID
			DeferCleanup(func() {
				ClusterH.EnsureDeleted(clusterA)
				ClusterH.EnsureDeleted(clusterB)
			})

			yamlA := renderK8sClusterYAML(map[string]string{
				"name": clusterA, "kubeconfig": kubeconfig,
			})
			r := ClusterH.Apply(yamlA)
			ExpectSuccess(r)

			yamlB := renderK8sClusterYAML(map[string]string{
				"name": clusterB, "kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yamlB)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterA, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
			r = ClusterH.WaitForPhase(clusterB, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			r = ClusterH.Get(clusterA)
			ExpectSuccess(r)
			cA := parseClusterJSON(r.Stdout)
			nsA := ClusterNamespace(cA.Metadata.Workspace, cA.Metadata.Name, cA.ID)

			r = ClusterH.Get(clusterB)
			ExpectSuccess(r)
			cB := parseClusterJSON(r.Stdout)
			nsB := ClusterNamespace(cB.Metadata.Workspace, cB.Metadata.Name, cB.ID)

			Expect(nsA).NotTo(Equal(nsB), "two clusters should have different namespaces")

			k8sH := NewK8sHelper(kubeconfig)
			ctx := context.Background()
			Expect(k8sH.NamespaceExists(ctx, nsA)).To(BeTrue())
			Expect(k8sH.NamespaceExists(ctx, nsB)).To(BeTrue())
		})
	})

	// --- Router Edit ---

	Describe("Router Edit", Ordered, Label("edit"), func() {
		var (
			clusterName string
			kubeconfig  string
			k8sH        *K8sHelper
			namespace   string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()
			clusterName = "e2e-k8s-rt-edit-" + Cfg.RunID

			yaml := renderK8sClusterYAML(map[string]string{
				"name":            clusterName,
				"kubeconfig":      kubeconfig,
				"router_cpu":      "1",
				"router_memory":   "2Gi",
				"router_replicas": "1",
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			k8sH = NewK8sHelper(kubeconfig)

			r = ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			namespace = ClusterNamespace(c.Metadata.Workspace, c.Metadata.Name, c.ID)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should update router CPU and verify K8s deployment", Label("C2612833"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			yaml := renderK8sClusterYAML(map[string]string{
				"name":          clusterName,
				"kubeconfig":    kubeconfig,
				"router_cpu":    "500m",
				"router_memory": "2Gi",
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, "router")
			Expect(err).NotTo(HaveOccurred(), "router deployment should exist")
			Expect(d.Spec.Template.Spec.Containers).NotTo(BeEmpty())

			cpu := d.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu()
			Expect(cpu.String()).To(Equal("500m"))
		})

		It("should update router memory and verify K8s deployment", Label("C2612835"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			yaml := renderK8sClusterYAML(map[string]string{
				"name":          clusterName,
				"kubeconfig":    kubeconfig,
				"router_cpu":    "500m",
				"router_memory": "1Gi",
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, "router")
			Expect(err).NotTo(HaveOccurred(), "router deployment should exist")
			Expect(d.Spec.Template.Spec.Containers).NotTo(BeEmpty())

			mem := d.Spec.Template.Spec.Containers[0].Resources.Requests.Memory()
			Expect(mem.String()).To(Equal("1Gi"))
		})

		It("should update router replicas and verify K8s deployment", Label("C2612837"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			yaml := renderK8sClusterYAML(map[string]string{
				"name":            clusterName,
				"kubeconfig":      kubeconfig,
				"router_cpu":      "500m",
				"router_memory":   "1Gi",
				"router_replicas": "2",
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)

			ctx := context.Background()
			d, err := k8sH.GetDeployment(ctx, namespace, "router")
			Expect(err).NotTo(HaveOccurred(), "router deployment should exist")
			Expect(*d.Spec.Replicas).To(Equal(int32(2)))
		})

		It("should reject invalid router CPU value", Label("C2612834"), func() {
			yaml := renderK8sClusterYAML(map[string]string{
				"name":          clusterName,
				"kubeconfig":    kubeconfig,
				"router_cpu":    "invalid-cpu",
				"router_memory": "1Gi",
			})
			r := ClusterH.Apply(yaml)
			ExpectFailed(r)
		})

		It("should reject invalid router memory value", Label("C2612836"), func() {
			yaml := renderK8sClusterYAML(map[string]string{
				"name":          clusterName,
				"kubeconfig":    kubeconfig,
				"router_cpu":    "500m",
				"router_memory": "invalid-mem",
			})
			r := ClusterH.Apply(yaml)
			ExpectFailed(r)
		})

		It("should reject invalid router replicas value", Label("C2612838"), func() {
			yaml := renderK8sClusterYAML(map[string]string{
				"name":            clusterName,
				"kubeconfig":      kubeconfig,
				"router_cpu":      "500m",
				"router_memory":   "1Gi",
				"router_replicas": "-1",
			})
			r := ClusterH.Apply(yaml)
			ExpectFailed(r)
		})
	})

	// --- Model Cache Edit ---

	Describe("Model Cache Edit", Ordered, Label("edit"), func() {
		var (
			clusterName string
			kubeconfig  string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()

			if profile.ModelCache.NFSServer == "" {
				Skip("ModelCache.NFSServer not configured in profile")
			}

			clusterName = "e2e-k8s-mc-edit-" + Cfg.RunID

			nfsCacheYAML := fmt.Sprintf("    model_caches:\n      - name: test-cache\n        nfs:\n          server: \"%s\"\n          path: \"%s\"",
				profile.ModelCache.NFSServer,
				profile.ModelCache.NFSPath)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": nfsCacheYAML,
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should update NFS server config", Label("C2612840"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			nfsCacheYAML := fmt.Sprintf("    model_caches:\n      - name: test-cache-updated\n        nfs:\n          server: \"%s\"\n          path: \"%s\"",
				profile.ModelCache.NFSServer, profile.ModelCache.NFSPath)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": nfsCacheYAML,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		It("should update NFS path", Label("C2612841"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			nfsCacheYAML := fmt.Sprintf("    model_caches:\n      - name: test-cache\n        nfs:\n          server: \"%s\"\n          path: \"%s/subdir\"",
				profile.ModelCache.NFSServer, profile.ModelCache.NFSPath)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": nfsCacheYAML,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		It("should switch to HostPath model cache", Label("C2612842"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			hostPathYAML := "    model_caches:\n      - name: test-cache\n        host_path:\n          path: /opt/neutree/model-cache-test"

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": hostPathYAML,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		It("should remove model cache", Label("C2612845"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})
	})

	// --- PVC Model Cache Edit ---

	Describe("PVC Model Cache Edit", Ordered, Label("edit"), func() {
		var (
			clusterName string
			kubeconfig  string
		)

		BeforeAll(func() {
			kubeconfig = requireK8sEnv()

			if profile.ModelCache.PVCStorageClass == "" {
				Skip("ModelCache.PVCStorageClass not configured in profile")
			}

			clusterName = "e2e-k8s-mc-pvc-" + Cfg.RunID

			pvcYAML := fmt.Sprintf("    model_caches:\n      - name: test-pvc-cache\n        pvc:\n          storageClassName: \"%s\"\n          resources:\n            requests:\n              storage: 10Gi", profile.ModelCache.PVCStorageClass)

			yaml := renderK8sClusterYAML(map[string]string{
				"name":              clusterName,
				"kubeconfig":        kubeconfig,
				"model_caches_yaml": pvcYAML,
			})

			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})

		AfterAll(func() {
			ClusterH.EnsureDeleted(clusterName)
		})

		It("should create PVC with correct spec (AccessModes, Size, VolumeMode) and cluster Running", Label("C2612780"), func() {
			By("Verifying cluster is Running with PVC model cache")
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			Expect(c.Status.Phase).To(BeEquivalentTo("Running"))

			By("Verifying PVC spec")
			k8sH := NewK8sHelper(kubeconfig)
			ns := ClusterNamespace(c.Metadata.Workspace, c.Metadata.Name, c.ID)

			ctx := context.Background()
			pvc, err := k8sH.GetPVC(ctx, ns, "models-cache-test-pvc-cache")
			Expect(err).NotTo(HaveOccurred(), "PVC models-cache-test-pvc-cache should exist")

			Expect(*pvc.Spec.StorageClassName).To(Equal(profile.ModelCache.PVCStorageClass))

			Expect(pvc.Spec.AccessModes).To(ContainElement(corev1.ReadWriteMany),
				"PVC default AccessModes should include ReadWriteMany")

			storage := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
			Expect(storage.String()).To(Equal("10Gi"),
				"PVC default size should be 10Gi")

			filesystem := corev1.PersistentVolumeFilesystem
			Expect(pvc.Spec.VolumeMode).To(Equal(&filesystem),
				"PVC default volumeMode should be Filesystem")
		})

		It("should create modelcache-config ConfigMap", Label("C2623077"), func() {
			k8sH := NewK8sHelper(kubeconfig)

			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			c := parseClusterJSON(r.Stdout)
			ns := ClusterNamespace(c.Metadata.Workspace, c.Metadata.Name, c.ID)
			cmName := fmt.Sprintf("neutree-%s-modelcache-config", c.Metadata.Name)

			ctx := context.Background()
			_, err := k8sH.GetConfigMap(ctx, ns, cmName)
			Expect(err).NotTo(HaveOccurred(), "deploy config CM %s should exist", cmName)
		})

		It("should remove PVC model cache and reach Running", Label("C2612845"), func() {
			r := ClusterH.Get(clusterName)
			ExpectSuccess(r)
			oldHash := parseClusterJSON(r.Stdout).Status.ObservedSpecHash

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": kubeconfig,
			})
			r = ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.WaitForSpecChange(clusterName, oldHash, IntermediatePhaseTimeout)

			r = ClusterH.WaitForPhase(clusterName, v1.ClusterPhaseRunning, TerminalPhaseTimeout)
			ExpectSuccess(r)
		})
	})

	// --- Configuration Anomaly ---

	Describe("Configuration Anomaly", Label("error"), func() {

		It("should stay Initializing when kubeconfig is invalid", Label("C2613153"), func() {
			clusterName := "e2e-k8s-badcfg-" + Cfg.RunID
			DeferCleanup(func() { ClusterH.EnsureDeleted(clusterName) })

			yaml := renderK8sClusterYAML(map[string]string{
				"name":       clusterName,
				"kubeconfig": base64.StdEncoding.EncodeToString([]byte("invalid-kubeconfig-yaml")),
			})
			r := ClusterH.Apply(yaml)
			ExpectSuccess(r)

			ClusterH.EventuallyInPhase(clusterName, v1.ClusterPhaseInitializing, "failed to create REST config", IntermediatePhaseTimeout)
		})
	})
})
