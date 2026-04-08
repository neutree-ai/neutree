package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// CPHelper provides control plane operations via SSH.
type CPHelper struct {
	host       string
	user       string
	keyFile    string
	composeDir string
	apiPort    int
	cliBin     string // remote path to neutree-cli binary
}

// NewCPHelper creates a CPHelper from profile config.
func NewCPHelper() *CPHelper {
	return &CPHelper{
		host:       profileCPHost(),
		user:       profileCPSSHUser(),
		keyFile:    profileCPSSHKeyPath(),
		composeDir: profileCPComposeDir(),
		apiPort:    profileCPAPIPort(),
		cliBin:     profileCPComposeDir() + "/neutree-cli",
	}
}

// requireCPEnv skips the test if control plane config is missing.
func requireCPEnv() *CPHelper {
	if profileCPHost() == "" {
		Skip("control_plane.host not configured in profile")
	}

	if profileCPSSHKeyPath() == "" {
		Skip("control_plane.ssh_key not configured in profile")
	}

	if profileCPVersion() == "" {
		Skip("control_plane.version not configured in profile")
	}

	return NewCPHelper()
}

// RunSSHCmd executes a command on the control plane VM via SSH.
func (cp *CPHelper) RunSSHCmd(command string) CLIResult {
	return RunSSH(cp.user, cp.host, cp.keyFile, command)
}

// RunCLI executes neutree-cli on the control plane VM.
// NEUTREE_LAUNCH_WORK_DIR is set to composeDir so launch/cleanup use the correct directory.
func (cp *CPHelper) RunCLI(args ...string) CLIResult {
	cmd := fmt.Sprintf("NEUTREE_LAUNCH_WORK_DIR=%s %s %s",
		cp.composeDir, cp.cliBin, strings.Join(args, " "))
	return cp.RunSSHCmd(cmd)
}

// DeployCLIBinary deploys neutree-cli to the VM.
// If control_plane.cli_url is set, downloads from URL; otherwise compiles from source.
func (cp *CPHelper) DeployCLIBinary() {
	if url := profileCPCLIURL(); url != "" {
		cp.downloadCLIBinary(url)
	} else {
		cp.compileCLIBinary()
	}
}

// downloadCLIBinary downloads a pre-built CLI binary to the VM.
func (cp *CPHelper) downloadCLIBinary(url string) {
	By("Downloading CLI binary from: " + url)

	r := cp.RunSSHCmd(fmt.Sprintf("curl -sfL -o %s '%s' && chmod +x %s", cp.cliBin, url, cp.cliBin))
	Expect(r.ExitCode).To(Equal(0),
		"failed to download CLI binary: %s", r.Stderr)
}

// compileCLIBinary compiles neutree-cli locally and scp to the VM.
func (cp *CPHelper) compileCLIBinary() {
	By("Building neutree-cli binary from source")

	tmpDir, err := os.MkdirTemp("", "e2e-cp-cli-*")
	Expect(err).NotTo(HaveOccurred())

	defer os.RemoveAll(tmpDir)

	localBin := filepath.Join(tmpDir, "neutree-cli")
	projectRoot := filepath.Join(".", "..", "..")

	buildCmd := exec.Command("go", "build", "-o", localBin, "./cmd/neutree-cli/") //nolint:gosec // e2e test helper
	buildCmd.Dir = projectRoot

	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	buildCmd.Stdout = GinkgoWriter
	buildCmd.Stderr = GinkgoWriter
	Expect(buildCmd.Run()).To(Succeed(), "failed to cross-compile neutree-cli for linux")

	By("SCP neutree-cli to control plane VM")

	scpCmd := exec.Command("scp", //nolint:gosec // e2e test helper
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", cp.keyFile,
		localBin,
		fmt.Sprintf("%s@%s:%s", cp.user, cp.host, cp.cliBin),
	)
	scpCmd.Stdout = GinkgoWriter
	scpCmd.Stderr = GinkgoWriter
	Expect(scpCmd.Run()).To(Succeed(), "failed to scp neutree-cli to VM")

	By("Making binary executable")

	r := cp.RunSSHCmd("chmod +x " + cp.cliBin)
	Expect(r.ExitCode).To(Equal(0), "chmod failed: %s", r.Stderr)
}

// CleanAll runs cleanup --remove-data for both components and removes deploy artifacts.
// With NEUTREE_LAUNCH_WORK_DIR={composeDir}, launch creates:
//
//	{composeDir}/neutree-core/docker-compose.yml
//	{composeDir}/obs-stack/docker-compose.yml
func (cp *CPHelper) CleanAll() {
	// Best-effort cleanup, ignore errors
	cp.RunCLI("cleanup", "neutree-core", "--force", "--remove-data")
	cp.RunCLI("cleanup", "obs-stack", "--force", "--remove-data")
	// Remove the generated subdirectories (but keep composeDir itself and CLI binaries)
	cp.RunSSHCmd("rm -rf " + cp.composeDir + "/neutree-core " + cp.composeDir + "/obs-stack")
	// Remove any leftover docker volumes from neutree-core/obs-stack projects
	cp.RunSSHCmd("docker volume ls -q --filter name=neutree-core | xargs -r docker volume rm 2>/dev/null; " +
		"docker volume ls -q --filter name=obs-stack | xargs -r docker volume rm 2>/dev/null; true")
}

// DownloadOfflineImages downloads the offline image archive to the VM.
func (cp *CPHelper) DownloadOfflineImages(url, destPath string) {
	By("Downloading offline images to VM: " + url)

	r := cp.RunSSHCmd(fmt.Sprintf("curl -sfL -o %s '%s'", destPath, url))
	Expect(r.ExitCode).To(Equal(0),
		"failed to download offline images: %s", r.Stderr)
}

// ImportControlPlane runs `neutree-cli import controlplane --local` to load images into local docker.
func (cp *CPHelper) ImportControlPlane(packagePath string) {
	By("Importing control plane images: " + packagePath)

	r := cp.RunCLI("import", "controlplane", "-p", packagePath, "--local")
	Expect(r.ExitCode).To(Equal(0),
		"failed to import control plane images:\nstdout: %s\nstderr: %s", r.Stdout, r.Stderr)
}

// CheckContainerRunning verifies a container name pattern is running via docker ps.
func (cp *CPHelper) CheckContainerRunning(namePattern string) bool {
	r := cp.RunSSHCmd(fmt.Sprintf("docker ps --format '{{.Names}}' | grep -q '%s'", namePattern))
	return r.ExitCode == 0
}

// ListContainers returns running container names.
func (cp *CPHelper) ListContainers() string {
	r := cp.RunSSHCmd("docker ps --format '{{.Names}}'")
	return r.Stdout
}

// ListVolumes returns docker volume names.
func (cp *CPHelper) ListVolumes() string {
	r := cp.RunSSHCmd("docker volume ls --format '{{.Name}}'")
	return r.Stdout
}

// CurlAPI sends a curl request to the neutree-api on the VM using the node IP.
func (cp *CPHelper) CurlAPI(path string) CLIResult {
	url := fmt.Sprintf("http://%s:%d%s", cp.host, cp.apiPort, path)
	return cp.RunSSHCmd(fmt.Sprintf("curl -sf --max-time 10 '%s'", url))
}

// APIURL returns the external API URL for the control plane VM.
func (cp *CPHelper) APIURL() string {
	return fmt.Sprintf("http://%s:%d", cp.host, cp.apiPort)
}

// DownloadOldCLI downloads the old version CLI binary to the VM.
func (cp *CPHelper) DownloadOldCLI(url string) {
	oldBin := cp.composeDir + "/neutree-cli-old"

	By("Downloading old CLI binary: " + url)

	r := cp.RunSSHCmd(fmt.Sprintf("curl -sfL -o %s '%s' && chmod +x %s", oldBin, url, oldBin))
	Expect(r.ExitCode).To(Equal(0),
		"failed to download old CLI: %s", r.Stderr)
}

// RunOldCLI executes the old version neutree-cli on the VM.
func (cp *CPHelper) RunOldCLI(args ...string) CLIResult {
	oldBin := cp.composeDir + "/neutree-cli-old"
	cmd := fmt.Sprintf("NEUTREE_LAUNCH_WORK_DIR=%s %s %s",
		cp.composeDir, oldBin, strings.Join(args, " "))

	return cp.RunSSHCmd(cmd)
}

// launchArgs builds common launch neutree-core arguments with version, mirror registry, and admin password.
func (cp *CPHelper) launchArgs(version, jwtSecret string, extraArgs ...string) []string {
	args := []string{
		"launch", "neutree-core",
		"--jwt-secret", jwtSecret,
		"--version", version,
	}

	if mr := profileCPMirrorRegistry(); mr != "" {
		args = append(args, "--mirror-registry", mr)
	}

	if rp := profileCPRegistryProject(); rp != "" {
		args = append(args, "--registry-project", rp)
	}

	// Admin password from auth profile (required for first-time setup)
	if pw := profile.Auth.Password; pw != "" {
		args = append(args, "--admin-password", pw)
	}

	args = append(args, extraArgs...)

	return args
}

// LaunchVersion launches neutree-core with a specific version using the given CLI binary (old or new).
// For old CLI, only basic flags (--jwt-secret, --version, --admin-password) are used;
// mirror registry flags may not be supported by older CLI versions.
func (cp *CPHelper) LaunchVersion(useOldCLI bool, version, jwtSecret string, extraArgs ...string) CLIResult {
	var args []string

	if useOldCLI {
		// Old CLI supports --mirror-registry but not --registry-project.
		// Combine them as --mirror-registry <registry>/<project>.
		args = []string{
			"launch", "neutree-core",
			"--jwt-secret", jwtSecret,
			"--version", version,
		}

		mr := profileCPMirrorRegistry()
		rp := profileCPRegistryProject()

		if mr != "" {
			if rp != "" {
				args = append(args, "--mirror-registry", mr+"/"+rp)
			} else {
				args = append(args, "--mirror-registry", mr)
			}
		}

		if pw := profile.Auth.Password; pw != "" {
			args = append(args, "--admin-password", pw)
		}

		args = append(args, extraArgs...)
	} else {
		args = cp.launchArgs(version, jwtSecret, extraArgs...)
	}

	By(fmt.Sprintf("Launching neutree-core: %s", strings.Join(args, " ")))

	if useOldCLI {
		return cp.RunOldCLI(args...)
	}

	return cp.RunCLI(args...)
}
