package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	testAPISvcName = "neutree-api-service"

	contourNamespace = "sks-system-contour"
	contourEnvoySvc  = "contour-envoy"
)

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

	// --- Custom install: public Ingress endpoints via Contour ---

	Describe("Ingress Deploy", Ordered, Label("ingress"), func() {
		var (
			ctx          = context.Background()
			apiHost      string
			kongHost     string
			grafanaHost  string
			vminsertHost string
			setValues    []string
			resources    []unstructured.Unstructured
		)

		BeforeAll(func() {
			h.CleanAll()

			apiHost = fmt.Sprintf("api-%s.neutree-e2e.local", Cfg.RunID)
			kongHost = fmt.Sprintf("kong-%s.neutree-e2e.local", Cfg.RunID)
			grafanaHost = fmt.Sprintf("grafana-%s.neutree-e2e.local", Cfg.RunID)
			vminsertHost = fmt.Sprintf("vminsert-%s.neutree-e2e.local", Cfg.RunID)

			ingressValues := []string{
				"api.ingress.enabled=true",
				"api.ingress.className=contour",
				"api.ingress.host=" + apiHost,
				"kong.ingress.enabled=true",
				"kong.ingress.className=contour",
				"kong.ingress.host=" + kongHost,
				"grafana.ingress.enabled=true",
				"grafana.ingress.ingressClassName=contour",
				"grafana.ingress.hosts[0]=" + grafanaHost,
				"grafana.grafana\\.ini.server.root_url=http://" + grafanaHost,
				"victoria-metrics-cluster.vminsert.ingress.enabled=true",
				"victoria-metrics-cluster.vminsert.ingress.ingressClassName=contour",
				"victoria-metrics-cluster.vminsert.ingress.hosts[0].name=" + vminsertHost,
				"victoria-metrics-cluster.vminsert.ingress.hosts[0].path[0]=/insert",
			}
			setValues = append(baseValues, ingressValues...)

			By("Installing with Contour Ingress values")
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

		It("should expose public ingress endpoints and wire runtime URLs", Label("C2721968"), func() {
			// TestRail: C2721968
			expectIngressBackend(ctx, h, "neutree-api", apiHost, "/", "neutree-api-service", 3000, "")
			expectIngressBackend(ctx, h, "neutree-kong-proxy", kongHost, "/", "neutree-kong-proxy", 80, "")
			expectIngressBackend(ctx, h, "neutree-grafana", grafanaHost, "/", "neutree-grafana", 80, "")
			expectIngressBackend(ctx, h, "neutree-victoria-metrics-cluster-vminsert",
				vminsertHost, "/insert", "neutree-victoria-metrics-cluster-vminsert", 0, "http")

			expectDeploymentArg(ctx, h, "neutree-api", "--grafana-url=http://"+grafanaHost)
			expectDeploymentArg(ctx, h, "neutree-core", "--gateway-proxy-url=http://"+kongHost)
			expectDeploymentArg(ctx, h, "neutree-vmagent",
				"--remoteWrite.url=http://"+vminsertHost+"/insert/0/prometheus/")

			baseURL := contourHTTPBaseURL(ctx, h.K8s)
			expectHostGETStatus(baseURL, apiHost, "/health", http.StatusOK)
			expectHostGETStatus(baseURL, grafanaHost, "/api/health", http.StatusOK)
			expectHostGETStatus(baseURL, vminsertHost, "/health", http.StatusOK)
		})
	})
})

func expectIngressBackend(
	ctx context.Context,
	h *K8sCPHelper,
	name string,
	host string,
	path string,
	serviceName string,
	servicePort int32,
	servicePortName string,
) {
	ing, err := h.K8s.GetIngress(ctx, h.Namespace, name)
	Expect(err).NotTo(HaveOccurred(), "ingress %s should exist", name)
	Expect(ing.Spec.IngressClassName).NotTo(BeNil(), "ingress %s should set ingressClassName", name)
	Expect(*ing.Spec.IngressClassName).To(Equal("contour"), "ingress %s should use contour", name)

	rule := findIngressRule(ing, host)
	Expect(rule).NotTo(BeNil(), "ingress %s should route host %s", name, host)
	Expect(rule.HTTP).NotTo(BeNil(), "ingress %s rule for host %s should have HTTP paths", name, host)

	pathRule := findIngressPath(rule.HTTP.Paths, path)
	Expect(pathRule).NotTo(BeNil(), "ingress %s host %s should route path %s", name, host, path)
	Expect(pathRule.PathType).To(Equal(networkingv1.PathTypePrefix), "ingress %s path %s pathType", name, path)
	Expect(pathRule.Backend.Service).NotTo(BeNil(), "ingress %s path %s should use a service backend", name, path)
	Expect(pathRule.Backend.Service.Name).To(Equal(serviceName), "ingress %s backend service", name)

	if servicePortName != "" {
		Expect(pathRule.Backend.Service.Port.Name).To(Equal(servicePortName), "ingress %s backend service port", name)
	} else {
		Expect(pathRule.Backend.Service.Port.Number).To(Equal(servicePort), "ingress %s backend service port", name)
	}
}

func findIngressRule(ing *networkingv1.Ingress, host string) *networkingv1.IngressRule {
	for i := range ing.Spec.Rules {
		if ing.Spec.Rules[i].Host == host {
			return &ing.Spec.Rules[i]
		}
	}

	return nil
}

func findIngressPath(paths []networkingv1.HTTPIngressPath, path string) *networkingv1.HTTPIngressPath {
	for i := range paths {
		if paths[i].Path == path {
			return &paths[i]
		}
	}

	return nil
}

func expectDeploymentArg(ctx context.Context, h *K8sCPHelper, deploymentName, expectedArg string) {
	deploy, err := h.K8s.GetDeployment(ctx, h.Namespace, deploymentName)
	Expect(err).NotTo(HaveOccurred(), "deployment %s should exist", deploymentName)

	var args []string
	for _, container := range deploy.Spec.Template.Spec.Containers {
		args = append(args, container.Args...)
	}

	Expect(args).To(ContainElement(expectedArg), "deployment %s should include %s", deploymentName, expectedArg)
}

func contourHTTPBaseURL(ctx context.Context, k8s *K8sHelper) string {
	svc, err := k8s.GetService(ctx, contourNamespace, contourEnvoySvc)
	Expect(err).NotTo(HaveOccurred(), "Contour Envoy service should exist")

	var nodePort int32
	for _, port := range svc.Spec.Ports {
		if port.Name == "http" || port.Port == 80 {
			nodePort = port.NodePort

			break
		}
	}
	Expect(nodePort).NotTo(BeZero(), "Contour Envoy service should expose HTTP NodePort")

	nodes, err := k8s.ListNodes(ctx)
	Expect(err).NotTo(HaveOccurred(), "should list K8s nodes")

	for _, node := range nodes {
		if !nodeReady(node) {
			continue
		}

		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP && address.Address != "" {
				return fmt.Sprintf("http://%s:%d", address.Address, nodePort)
			}
		}
	}

	Fail("no ready node with InternalIP found for Contour NodePort")

	return ""
}

func nodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

func expectHostGETStatus(baseURL, host, path string, expectedStatus int) {
	client := &http.Client{Timeout: 10 * time.Second}

	Eventually(func() int {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			return 0
		}

		req.Host = host

		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)

		return resp.StatusCode
	}, 2*time.Minute, 5*time.Second).Should(Equal(expectedStatus),
		"%s should return %d through %s with Host %s", path, expectedStatus, baseURL, host)
}
