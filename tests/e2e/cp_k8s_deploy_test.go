package e2e

import (
	"context"
	"fmt"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const testAPISvcName = "neutree-api-service"

var _ = Describe("K8s Control Plane Deploy", Ordered, Label("control-plane", "k8s-deploy"), func() {
	var (
		h          *K8sCPHelper
		baseValues []string
	)

	BeforeAll(func() {
		cfg := requireK8sCPProfile()
		h = NewK8sCPHelper(cfg)

		baseValues = []string{
			"jwtSecret=e2e-k8s-jwt-secret-long-enough-" + Cfg.RunID,
			"adminPassword=" + profile.Auth.Password,
			"grafana.persistence.size=2Gi",
			"vmagent.persistence.size=2Gi",
			"db.persistence.size=2Gi",
		}
	})

	// --- Default install (no custom values) ---

	Describe("Online Deploy", Ordered, Label("online"), func() {
		BeforeAll(func() {
			h.CleanAll()
		})

		AfterAll(func() {
			h.CleanAll()
		})

		It("should deploy with online images and all resources healthy", Label("C2587857", "C2587861"), func() {
			r := h.HelmInstall(baseValues...)
			ExpectSuccess(r)

			By("Waiting for all helm resources to be healthy")
			resources := h.HelmTemplate(baseValues...)
			Eventually(func() error {
				return h.CheckHelmDeployed(resources)
			}, 15*time.Minute, 10*time.Second).Should(Succeed(),
				"all resources should be healthy")
		})
	})

	// --- Custom install: LB + private registry + custom DB in one deploy ---

	Describe("Custom Deploy", Ordered, Label("custom-deploy"), func() {
		var (
			ctx        = context.Background()
			secretName = "e2e-pull-secret"
			customPwd  string
			setValues  []string
			resources  []unstructured.Unstructured
		)

		BeforeAll(func() {
			h.CleanAll()

			if profileCPMirrorRegistry() == "" {
				Skip("control_plane.mirror_registry not configured, skipping custom deploy tests")
			}

			customPwd = "e2eCustomDb" + Cfg.RunID

			customValues := append([]string{
				"api.service.type=LoadBalancer",
				"global.imagePullSecrets[0].name=" + secretName,
				"db.password=" + customPwd,
			}, helmMirrorRegistrySetValues()...)
			setValues = append(baseValues, customValues...)

			By("Creating docker-registry secret")
			err := h.K8s.CreateNamespace(ctx, h.Namespace)
			Expect(err).NotTo(HaveOccurred())

			err = h.K8s.CreateDockerRegistrySecret(ctx, h.Namespace, secretName,
				profileCPMirrorRegistry(),
				profile.ImageRegistry.Username,
				profile.ImageRegistry.Password,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Installing with all custom values")
			r := h.HelmInstall(setValues...)
			ExpectSuccess(r)

			By("Waiting for all helm resources to be healthy")
			resources = h.HelmTemplate(setValues...)
			Eventually(func() error {
				return h.CheckHelmDeployed(resources)
			}, 15*time.Minute, 10*time.Second).Should(Succeed(),
				"all resources should be healthy")
		})

		AfterAll(func() {
			h.CleanAll()
		})

		It("should have LoadBalancer service accessible", Label("C2587865"), func() {
			svc, err := h.K8s.GetService(ctx, h.Namespace, testAPISvcName)
			Expect(err).NotTo(HaveOccurred())
			Expect(svc.Spec.Type).To(Equal(corev1.ServiceTypeLoadBalancer),
				"API service should be LoadBalancer type")
			Expect(svc.Status.LoadBalancer.Ingress).NotTo(BeEmpty(),
				"API service should have external IP assigned")

			externalIP := svc.Status.LoadBalancer.Ingress[0].IP
			if externalIP == "" {
				externalIP = svc.Status.LoadBalancer.Ingress[0].Hostname
			}

			var port int32

			for _, sp := range svc.Spec.Ports {
				if sp.Name == "http" {
					port = sp.Port

					break
				}
			}

			Expect(port).NotTo(BeZero(), "API service should have an http port")

			healthURL := fmt.Sprintf("http://%s:%d/health", externalIP, port)
			client := &http.Client{Timeout: 10 * time.Second}

			Eventually(func() int {
				resp, err := client.Get(healthURL)
				if err != nil {
					return 0
				}
				defer resp.Body.Close()

				return resp.StatusCode
			}, 1*time.Minute, 5*time.Second).Should(Equal(http.StatusOK),
				"API should be accessible via LoadBalancer at %s", healthURL)
		})

		It("should have imagePullSecrets on all workloads", Label("C2611274"), func() {
			for _, res := range resources {
				kind := res.GetKind()
				name := res.GetName()

				var pullSecrets []corev1.LocalObjectReference

				switch kind {
				case kindDeployment:
					deploy, err := h.K8s.GetDeployment(ctx, h.Namespace, name)
					Expect(err).NotTo(HaveOccurred())
					pullSecrets = deploy.Spec.Template.Spec.ImagePullSecrets

				case kindStatefulSet:
					sts, err := h.K8s.GetStatefulSet(ctx, h.Namespace, name)
					Expect(err).NotTo(HaveOccurred())
					pullSecrets = sts.Spec.Template.Spec.ImagePullSecrets

				case kindJob:
					job, err := h.K8s.GetJob(ctx, h.Namespace, name)
					Expect(err).NotTo(HaveOccurred())
					pullSecrets = job.Spec.Template.Spec.ImagePullSecrets

				default:
					continue
				}

				found := false
				for _, ps := range pullSecrets {
					if ps.Name == secretName {
						found = true

						break
					}
				}

				Expect(found).To(BeTrue(),
					"%s %s should have imagePullSecrets with %s", kind, name, secretName)
			}
		})

		It("should have custom image registry prefix on all workloads", Label("C2611276"), func() {
			customRegistry := profileCPMirrorRegistry()
			if rp := profileCPRegistryProject(); rp != "" {
				customRegistry = customRegistry + "/" + rp
			}

			for _, res := range resources {
				switch res.GetKind() {
				case kindDeployment:
					deploy, err := h.K8s.GetDeployment(ctx, h.Namespace, res.GetName())
					Expect(err).NotTo(HaveOccurred())

					for _, c := range deploy.Spec.Template.Spec.Containers {
						Expect(c.Image).To(ContainSubstring(customRegistry),
							"deployment %s container %s image should use custom registry", res.GetName(), c.Name)
					}

					for _, c := range deploy.Spec.Template.Spec.InitContainers {
						Expect(c.Image).To(ContainSubstring(customRegistry),
							"deployment %s init-container %s image should use custom registry", res.GetName(), c.Name)
					}

				case kindStatefulSet:
					sts, err := h.K8s.GetStatefulSet(ctx, h.Namespace, res.GetName())
					Expect(err).NotTo(HaveOccurred())

					for _, c := range sts.Spec.Template.Spec.Containers {
						Expect(c.Image).To(ContainSubstring(customRegistry),
							"statefulset %s container %s image should use custom registry", res.GetName(), c.Name)
					}

				case kindJob:
					job, err := h.K8s.GetJob(ctx, h.Namespace, res.GetName())
					Expect(err).NotTo(HaveOccurred())

					for _, c := range job.Spec.Template.Spec.Containers {
						Expect(c.Image).To(ContainSubstring(customRegistry),
							"job %s container %s image should use custom registry", res.GetName(), c.Name)
					}
				}
			}
		})

		It("should connect to API with custom db.password", Label("C2642241"), func() {
			Eventually(func() error {
				return h.K8s.ServiceProxyGet(ctx, h.Namespace, testAPISvcName,
					fmt.Sprintf("%d", profileCPAPIPort()), "/health")
			}, 1*time.Minute, 3*time.Second).Should(Succeed(),
				"API should be accessible with custom db.password")
		})
	})
})
