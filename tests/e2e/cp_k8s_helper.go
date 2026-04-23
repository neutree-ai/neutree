package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	kindDeployment  = "Deployment"
	kindStatefulSet = "StatefulSet"
	kindJob         = "Job"
	kindService     = "Service"
)

// cpK8sConfig holds validated K8s control plane config.
type cpK8sConfig struct {
	Kubeconfig string
	Namespace  string
	ChartURL   string
}

// requireK8sCPProfile validates K8s control plane config and returns it.
// Skips the test if required fields are missing.
func requireK8sCPProfile() cpK8sConfig {
	cfg := cpK8sConfig{
		Kubeconfig: profileCPKubeconfig(),
		Namespace:  profileCPK8sNamespace(),
		ChartURL:   profileCPChartURL(),
	}

	if cfg.Kubeconfig == "" {
		Skip("control_plane.kubeconfig not configured in profile")
	}

	return cfg
}

// K8sCPHelper wraps K8sHelper with Helm operations for control plane deployment tests.
type K8sCPHelper struct {
	K8s        *K8sHelper
	Namespace  string
	kubeconfig string
	chartPath  string
}

// NewK8sCPHelper creates a K8sCPHelper from a validated cpK8sConfig.
func NewK8sCPHelper(cfg cpK8sConfig) *K8sCPHelper {
	chartPath, _ := filepath.Abs(filepath.Join("..", "..", "deploy", "chart", "neutree"))

	if cfg.ChartURL != "" {
		tmpDir, err := os.MkdirTemp("", "e2e-chart-*")
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() { os.RemoveAll(tmpDir) })

		tgzPath := filepath.Join(tmpDir, "neutree.tgz")

		By("Downloading helm chart from: " + cfg.ChartURL)
		cmd := exec.Command("curl", "-sfL", "-o", tgzPath, cfg.ChartURL)
		cmd.Stdout = GinkgoWriter
		cmd.Stderr = GinkgoWriter
		Expect(cmd.Run()).To(Succeed(), "failed to download chart from %s", cfg.ChartURL)

		chartPath = tgzPath
	}

	return &K8sCPHelper{
		K8s:        NewK8sHelperFromFile(cfg.Kubeconfig),
		kubeconfig: cfg.Kubeconfig,
		Namespace:  cfg.Namespace,
		chartPath:  chartPath,
	}
}

// helmArgs builds helm command skeleton (chart, kubeconfig, namespace) with caller-provided --set overrides.
func (h *K8sCPHelper) helmArgs(command string, setValues ...string) []string {
	args := []string{
		command, "neutree", h.chartPath,
		"--kubeconfig", h.kubeconfig,
		"--namespace", h.Namespace,
	}

	if command == "install" {
		args = append(args, "--create-namespace")
	}

	for _, sv := range setValues {
		args = append(args, "--set", sv)
	}

	return args
}

// helmMirrorRegistrySetValues returns --set values for mirror registry from profile.
func helmMirrorRegistrySetValues() []string {
	mr := profileCPMirrorRegistry()
	if mr == "" {
		return nil
	}

	registry := mr
	if rp := profileCPRegistryProject(); rp != "" {
		registry = mr + "/" + rp
	}

	return []string{
		"global.image.registry=" + registry,
		"global.imageRegistry=" + registry,
	}
}

// HelmInstall runs helm install with the given --set values.
func (h *K8sCPHelper) HelmInstall(setValues ...string) CLIResult {
	args := h.helmArgs("install", setValues...)
	GinkgoWriter.Printf("helm %s\n", strings.Join(args, " "))

	return runHelm(args...)
}

// HelmUninstall removes the helm release.
func (h *K8sCPHelper) HelmUninstall() CLIResult {
	return runHelm(
		"uninstall", "neutree",
		"--kubeconfig", h.kubeconfig,
		"--namespace", h.Namespace,
		"--wait",
		"--timeout", "5m",
	)
}

// CleanAll uninstalls helm release and deletes the namespace. Best-effort, ignores errors.
func (h *K8sCPHelper) CleanAll() {
	h.HelmUninstall()
	_ = h.K8s.DeleteNamespace(context.Background(), h.Namespace) // best-effort
	h.K8s.WaitForNamespaceDeleted(context.Background(), h.Namespace, 5*time.Minute)
}

// HelmTemplate runs helm template with the same args as HelmInstall and returns
// all rendered Kubernetes resources as unstructured objects.
func (h *K8sCPHelper) HelmTemplate(setValues ...string) []unstructured.Unstructured {
	args := h.helmArgs("template", setValues...)
	r := runHelm(args...)
	ExpectWithOffset(1, r.ExitCode).To(Equal(0),
		"helm template failed:\nstderr: %s", r.Stderr)

	var resources []unstructured.Unstructured

	decoder := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(r.Stdout)), 4096)

	for {
		var obj unstructured.Unstructured
		if err := decoder.Decode(&obj); err != nil {
			if err == io.EOF {
				break
			}

			ExpectWithOffset(1, err).NotTo(HaveOccurred(),
				"failed to decode helm template manifest")
		}

		if obj.GetKind() == "" {
			continue
		}

		resources = append(resources, obj)
	}

	return resources
}

// CheckHelmDeployed checks all resources from HelmTemplate are healthy.
// Returns nil if all checks pass, or an error describing the first failure.
// Safe for use inside Eventually().
func (h *K8sCPHelper) CheckHelmDeployed(resources []unstructured.Unstructured) error {
	ctx := context.Background()
	ns := h.Namespace

	for _, obj := range resources {
		kind := obj.GetKind()
		name := obj.GetName()

		switch kind {
		case kindDeployment:
			deploy, err := h.K8s.GetDeployment(ctx, ns, name)
			if err != nil {
				return fmt.Errorf("deployment %s: %w", name, err)
			}

			if deploy.Spec.Replicas != nil && deploy.Status.ReadyReplicas != *deploy.Spec.Replicas {
				return fmt.Errorf("deployment %s: %d/%d replicas ready",
					name, deploy.Status.ReadyReplicas, *deploy.Spec.Replicas)
			}

		case kindStatefulSet:
			sts, err := h.K8s.GetStatefulSet(ctx, ns, name)
			if err != nil {
				return fmt.Errorf("statefulset %s: %w", name, err)
			}

			if sts.Spec.Replicas != nil && sts.Status.ReadyReplicas != *sts.Spec.Replicas {
				return fmt.Errorf("statefulset %s: %d/%d replicas ready",
					name, sts.Status.ReadyReplicas, *sts.Spec.Replicas)
			}

		case kindJob:
			job, err := h.K8s.GetJob(ctx, ns, name)
			if err != nil {
				return fmt.Errorf("job %s: %w", name, err)
			}

			if job.Status.Succeeded < 1 {
				return fmt.Errorf("job %s: not completed (succeeded=%d)", name, job.Status.Succeeded)
			}

		case kindService:
			svc, err := h.K8s.GetService(ctx, ns, name)
			if err != nil {
				return fmt.Errorf("service %s: %w", name, err)
			}

			if svc.Spec.Type == "LoadBalancer" && len(svc.Status.LoadBalancer.Ingress) == 0 {
				return fmt.Errorf("LoadBalancer service %s: no external IP assigned", name)
			}
		}
	}

	return nil
}

// runHelm executes a helm command and returns the result.
func runHelm(args ...string) CLIResult {
	cmd := exec.Command("helm", args...)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return CLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}
