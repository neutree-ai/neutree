package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// K8sCPHelper wraps K8sHelper with Helm operations for control plane deployment tests.
type K8sCPHelper struct {
	K8s        *K8sHelper
	kubeconfig string // file path (passed to helm --kubeconfig)
	namespace  string
	chartPath  string // local path to chart (downloaded .tgz or local dir)
}

// NewK8sCPHelper creates a K8sCPHelper from profile config.
func NewK8sCPHelper() *K8sCPHelper {
	kcPath := profileCPKubeconfig()
	ns := profileCPK8sNamespace()

	// Default chart path: local source
	chartPath, _ := filepath.Abs(filepath.Join("..", "..", "deploy", "chart", "neutree"))

	// If chart_url is configured, download it
	if url := profileCPChartURL(); url != "" {
		tmpDir, err := os.MkdirTemp("", "e2e-chart-*")
		Expect(err).NotTo(HaveOccurred())

		tgzPath := filepath.Join(tmpDir, "neutree.tgz")

		By("Downloading helm chart from: " + url)
		cmd := exec.Command("curl", "-sfL", "-o", tgzPath, url)
		cmd.Stdout = GinkgoWriter
		cmd.Stderr = GinkgoWriter
		Expect(cmd.Run()).To(Succeed(), "failed to download chart from %s", url)

		chartPath = tgzPath
	}

	return &K8sCPHelper{
		K8s:        NewK8sHelperFromFile(kcPath),
		kubeconfig: kcPath,
		namespace:  ns,
		chartPath:  chartPath,
	}
}

// requireK8sCPEnv skips the test if K8s control plane config is missing.
func requireK8sCPEnv() *K8sCPHelper {
	if profileCPKubeconfig() == "" {
		Skip("control_plane.kubeconfig not configured in profile")
	}

	return NewK8sCPHelper()
}

// HelmInstall runs helm install with the given --set values.
// Automatically injects global.image.registry and global.imageRegistry from profile.
func (h *K8sCPHelper) HelmInstall(setValues ...string) CLIResult {
	args := []string{
		"install", "neutree", h.chartPath,
		"--kubeconfig", h.kubeconfig,
		"--namespace", h.namespace,
		"--create-namespace",
	}

	// Auto-inject jwtSecret and adminPassword from profile
	args = append(args, "--set", "jwtSecret=e2e-k8s-jwt-secret-long-enough-"+Cfg.RunID)
	if pw := profile.Auth.Password; pw != "" {
		args = append(args, "--set", "adminPassword="+pw)
	}

	// Reduce PVC sizes for e2e (avoid storage quota issues)
	args = append(args,
		"--set", "grafana.persistence.size=2Gi",
		"--set", "vmagent.persistence.size=2Gi",
		"--set", "db.persistence.size=2Gi",
	)

	// Auto-inject mirror registry as global image registry.
	// registry_project is included — chart templates already have sub-paths
	// (e.g. kong/kong → {registry}/{project}/kong/kong).
	if mr := profileCPMirrorRegistry(); mr != "" {
		registry := mr
		if rp := profileCPRegistryProject(); rp != "" {
			registry = mr + "/" + rp
		}

		args = append(args, "--set", "global.image.registry="+registry)
		args = append(args, "--set", "global.imageRegistry="+registry)
	}

	for _, sv := range setValues {
		args = append(args, "--set", sv)
	}

	GinkgoWriter.Printf("helm %s\n", strings.Join(args, " "))

	return runHelm(args...)
}

// HelmUninstall removes the helm release.
func (h *K8sCPHelper) HelmUninstall() CLIResult {
	return runHelm(
		"uninstall", "neutree",
		"--kubeconfig", h.kubeconfig,
		"--namespace", h.namespace,
		"--wait",
		"--timeout", "5m",
	)
}

// CleanAll uninstalls helm release and deletes the namespace.
func (h *K8sCPHelper) CleanAll() {
	h.HelmUninstall()
	_ = h.K8s.DeleteNamespace(context.Background(), h.namespace)
	h.K8s.WaitForNamespaceDeleted(context.Background(), h.namespace, 2*time.Minute)
}

// Namespace returns the namespace used for deployment.
func (h *K8sCPHelper) Namespace() string {
	return h.namespace
}

// PortForwardStart starts port-forwarding a service and returns a cancel function.
func (h *K8sCPHelper) PortForwardStart(svcName string, localPort, remotePort int) (cancel func()) {
	cmd := exec.Command("kubectl", //nolint:gosec // e2e test helper
		"--kubeconfig", h.kubeconfig,
		"--namespace", h.namespace,
		"port-forward",
		fmt.Sprintf("svc/%s", svcName),
		fmt.Sprintf("%d:%d", localPort, remotePort),
	)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter

	Expect(cmd.Start()).To(Succeed(), "failed to start port-forward for %s", svcName)

	return func() {
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
		}
	}
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
