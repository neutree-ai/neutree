package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
)

var _ = Describe("K8s Control Plane Deploy", Ordered, Label("control-plane", "k8s-deploy"), func() {
	var h *K8sCPHelper

	BeforeAll(func() {
		h = requireK8sCPEnv()
	})

	// Each Describe is self-contained: install → verify → uninstall.

	Describe("Default DockerHub Deploy", Ordered, Label("dockerhub"), func() {
		BeforeAll(func() {
			h.CleanAll()
		})

		AfterAll(func() {
			h.CleanAll()
		})

		It("should deploy with default dockerhub images", Label("C2587857"), func() {
			r := h.HelmInstall(
			)
			ExpectCPSuccess(r)

			By("Waiting for all pods to be ready")
			h.K8s.WaitPodsReady(context.Background(), h.Namespace(), 15*time.Minute)
		})

		It("should deploy both management and monitoring components", Label("C2587861"), func() {
			pods, err := h.K8s.ListPods(context.Background(), h.Namespace(), "")
			Expect(err).NotTo(HaveOccurred())

			podNames := make([]string, 0, len(pods))
			for _, p := range pods {
				podNames = append(podNames, p.Name)
			}
			joined := fmt.Sprintf("%v", podNames)

			By("Verifying management components")
			for _, component := range []string{"api", "core", "postgres", "kong"} {
				Expect(joined).To(ContainSubstring(component),
					"management component %s should have a running pod", component)
			}

			By("Verifying monitoring components")
			for _, component := range []string{"grafana", "vmagent"} {
				Expect(joined).To(ContainSubstring(component),
					"monitoring component %s should have a running pod", component)
			}
		})
	})

	Describe("LoadBalancer Service", Ordered, Label("loadbalancer"), func() {
		BeforeAll(func() {
			h.CleanAll()
		})

		AfterAll(func() {
			h.CleanAll()
		})

		It("should deploy with LoadBalancer service type and be accessible", Label("C2587865"), func() {
			r := h.HelmInstall(
				"api.service.type=LoadBalancer",
			)
			ExpectCPSuccess(r)

			h.K8s.WaitPodsReady(context.Background(), h.Namespace(), 15*time.Minute)

			By("Finding API service with LoadBalancer type")
			ctx := context.Background()
			svcs, err := h.K8s.ListServices(ctx, h.Namespace(), "")
			Expect(err).NotTo(HaveOccurred())

			var apiSvcName string
			for _, svc := range svcs {
				if string(svc.Spec.Type) == "LoadBalancer" {
					apiSvcName = svc.Name

					break
				}
			}

			Expect(apiSvcName).NotTo(BeEmpty(),
				"should find a LoadBalancer service in namespace")

			By("Waiting for external IP to be assigned on: " + apiSvcName)
			var externalIP string
			Eventually(func() string {
				svc, err := h.K8s.GetService(ctx, h.Namespace(), apiSvcName)
				if err != nil {
					return ""
				}

				if len(svc.Status.LoadBalancer.Ingress) > 0 {
					if svc.Status.LoadBalancer.Ingress[0].IP != "" {
						return svc.Status.LoadBalancer.Ingress[0].IP
					}

					return svc.Status.LoadBalancer.Ingress[0].Hostname
				}

				return ""
			}, 3*time.Minute, 5*time.Second).ShouldNot(BeEmpty())

			svc, err := h.K8s.GetService(ctx, h.Namespace(), apiSvcName)
			Expect(err).NotTo(HaveOccurred())

			if len(svc.Status.LoadBalancer.Ingress) > 0 {
				externalIP = svc.Status.LoadBalancer.Ingress[0].IP
				if externalIP == "" {
					externalIP = svc.Status.LoadBalancer.Ingress[0].Hostname
				}
			}

			By("Verifying API is accessible via LoadBalancer IP: " + externalIP)
			port := int32(profileCPAPIPort())

			for _, sp := range svc.Spec.Ports {
				if sp.Name == "http" || sp.Port == int32(profileCPAPIPort()) {
					port = sp.Port

					break
				}
			}

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
	})

	Describe("Private Registry with imagePullSecrets", Ordered, Label("private-registry"), func() {
		BeforeAll(func() {
			h.CleanAll()

			if profileCPMirrorRegistry() == "" {
				Skip("control_plane.mirror_registry not configured, skipping private registry tests")
			}
		})

		AfterAll(func() {
			h.CleanAll()
		})

		It("should deploy with imagePullSecrets and pull from private registry", Label("C2611274"), func() {
			ctx := context.Background()
			secretName := "e2e-pull-secret"

			registry := profileCPMirrorRegistry()
			if rp := profileCPRegistryProject(); rp != "" {
				registry = registry + "/" + rp
			}

			By("Creating docker-registry secret for " + registry)
			err := h.K8s.CreateNamespace(ctx, h.Namespace())
			Expect(err).NotTo(HaveOccurred())

			err = h.K8s.CreateDockerRegistrySecret(ctx, h.Namespace(), secretName,
				profileCPMirrorRegistry(),
				profile.ImageRegistry.Username,
				profile.ImageRegistry.Password,
			)
			Expect(err).NotTo(HaveOccurred())

			By("Installing with imagePullSecrets")
			r := h.HelmInstall(
				"global.imagePullSecrets[0].name="+secretName,
			)
			ExpectCPSuccess(r)

			h.K8s.WaitPodsReady(ctx, h.Namespace(), 15*time.Minute)

			By("Verifying deployments have imagePullSecrets")
			deploys, err := h.K8s.ListDeployments(ctx, h.Namespace(), "")
			Expect(err).NotTo(HaveOccurred())
			Expect(deploys).NotTo(BeEmpty())

			foundSecret := false
			for _, d := range deploys {
				for _, ps := range d.Spec.Template.Spec.ImagePullSecrets {
					if ps.Name == secretName {
						foundSecret = true

						break
					}
				}

				if foundSecret {
					break
				}
			}

			Expect(foundSecret).To(BeTrue(),
				"at least one deployment should have imagePullSecrets with %s", secretName)

			By("Verifying all pods are running (image pull succeeded)")
			pods, err := h.K8s.ListPods(ctx, h.Namespace(), "")
			Expect(err).NotTo(HaveOccurred())

			for _, pod := range pods {
				Expect(pod.Status.Phase).To(
					BeElementOf(corev1.PodRunning, corev1.PodSucceeded),
					"pod %s should be Running or Succeeded, got %s", pod.Name, pod.Status.Phase)
			}
		})
	})

	Describe("Custom Image Registry", Ordered, Label("custom-registry"), func() {
		BeforeAll(func() {
			h.CleanAll()

			if profileCPMirrorRegistry() == "" {
				Skip("control_plane.mirror_registry not configured, skipping custom registry tests")
			}
		})

		AfterAll(func() {
			h.CleanAll()
		})

		It("should deploy with custom image registry prefix on all pods", Label("C2611276"), func() {
			ctx := context.Background()
			customRegistry := profileCPMirrorRegistry()
			if rp := profileCPRegistryProject(); rp != "" {
				customRegistry = customRegistry + "/" + rp
			}

			// HelmInstall already injects global.image.registry from profile,
			// so no extra --set needed. Just add imagePullSecrets.
			secretName := "e2e-pull-secret"
			_ = h.K8s.CreateNamespace(ctx, h.Namespace())
			_ = h.K8s.CreateDockerRegistrySecret(ctx, h.Namespace(), secretName,
				profileCPMirrorRegistry(),
				profile.ImageRegistry.Username,
				profile.ImageRegistry.Password,
			)

			r := h.HelmInstall(
				"global.imagePullSecrets[0].name="+secretName,
			)
			ExpectCPSuccess(r)
			h.K8s.WaitPodsReady(ctx, h.Namespace(), 15*time.Minute)

			By("Verifying pod images contain custom registry prefix")
			podImages := h.K8s.GetPodImages(ctx, h.Namespace())
			Expect(podImages).NotTo(BeEmpty())

			for podName, images := range podImages {
				for _, image := range images {
					Expect(image).To(ContainSubstring(customRegistry),
						"pod %s image %s should use custom registry %s",
						podName, image, customRegistry)
				}
			}
		})
	})

	Describe("Custom DB Password", Ordered, Label("custom-db"), func() {
		var cancelPF func()

		BeforeAll(func() {
			h.CleanAll()
		})

		AfterAll(func() {
			if cancelPF != nil {
				cancelPF()
			}

			h.CleanAll()
		})

		It("should deploy with custom db.password and API connects normally", Label("C2642241"), func() {
			customPwd := "e2eCustomDb" + Cfg.RunID

			r := h.HelmInstall(
				"db.password="+customPwd,
			)
			ExpectCPSuccess(r)

			h.K8s.WaitPodsReady(context.Background(), h.Namespace(), 15*time.Minute)

			By("Finding API service for port-forward")
			svcs, err := h.K8s.ListServices(context.Background(), h.Namespace(), "")
			Expect(err).NotTo(HaveOccurred())

			var apiSvc string
			for _, svc := range svcs {
				if strings.Contains(svc.Name, "api") {
					apiSvc = svc.Name

					break
				}
			}

			Expect(apiSvc).NotTo(BeEmpty(), "should find an API service")

			By("Port-forwarding to " + apiSvc)
			cancelPF = h.PortForwardStart(apiSvc, 19876, profileCPAPIPort())

			// Give port-forward a moment to establish
			time.Sleep(3 * time.Second)

			By("Verifying API is accessible (proves DB connection with custom password works)")
			client := &http.Client{Timeout: 10 * time.Second}

			Eventually(func() int {
				resp, err := client.Get("http://127.0.0.1:19876/health")
				if err != nil {
					return 0
				}
				defer resp.Body.Close()

				return resp.StatusCode
			}, 1*time.Minute, 3*time.Second).Should(Equal(http.StatusOK),
				"API should be accessible with custom db.password")
		})
	})
})
