package e2e

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"syscall"

	"github.com/Masterminds/semver/v3"
	"github.com/compose-spec/compose-go/loader"
	composetypes "github.com/compose-spec/compose-go/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
	cliutil "github.com/neutree-ai/neutree/cmd/neutree-cli/app/util"
)

// cpComposeConfig holds validated control plane docker-compose config.
type cpComposeConfig struct {
	Host       string
	SSHUser    string
	SSHKeyPath string
	ComposeDir string
	APIPort    int
	Version    string
}

// requireCPProfile validates control plane docker-compose config and returns it.
// Skips the test if required fields are missing.
// host == "" or host == "local" enables local mode (no SSH).
func requireCPProfile() cpComposeConfig {
	cfg := cpComposeConfig{
		Host:       profileCPHost(),
		SSHUser:    profileCPSSHUser(),
		SSHKeyPath: profileCPSSHKeyPath(),
		ComposeDir: profileCPComposeDir(),
		APIPort:    profileCPAPIPort(),
		Version:    profileCPVersion(),
	}

	if cfg.Version == "" {
		Skip("control_plane.version not configured in profile")
	}

	if cfg.ComposeDir == "" {
		Skip("control_plane.compose_dir not configured in profile")
	}

	if !cfg.IsLocal() && cfg.SSHKeyPath == "" {
		Skip("control_plane.ssh_key not configured in profile (required for remote mode)")
	}

	return cfg
}

// IsLocal returns true if this config targets the local machine.
func (cfg cpComposeConfig) IsLocal() bool {
	return cfg.Host == "" || cfg.Host == "local"
}

// CPHelper provides control plane operations locally or via SSH.
type CPHelper struct {
	local      bool
	host       string
	user       string
	keyFile    string
	composeDir string
	apiPort    int
	cliBin     string
}

// NewCPHelper creates a CPHelper from a validated cpComposeConfig.
func NewCPHelper(cfg cpComposeConfig) *CPHelper {
	h := &CPHelper{
		local:      cfg.IsLocal(),
		host:       cfg.Host,
		user:       cfg.SSHUser,
		keyFile:    cfg.SSHKeyPath,
		composeDir: cfg.ComposeDir,
		apiPort:    cfg.APIPort,
		cliBin:     cfg.ComposeDir + "/neutree-cli",
	}

	if h.local {
		ip, err := cliutil.GetHostIP()
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "failed to detect host IP for local mode")

		h.host = ip
	}

	return h
}

// RunCmd executes a command on the control plane machine.
// In local mode, runs via local shell; in remote mode, runs via SSH.
func (cp *CPHelper) RunCmd(command string) CLIResult {
	if cp.local {
		return runLocal(command)
	}

	return RunSSH(cp.user, cp.host, cp.keyFile, command)
}

// runLocal executes a command in the local shell.
func runLocal(command string) CLIResult {
	cmd := exec.Command("bash", "-c", command) //nolint:gosec // e2e test helper

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
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

// RunCLI executes neutree-cli on the control plane machine.
// NEUTREE_LAUNCH_WORK_DIR is set to composeDir so launch/cleanup use the correct directory.
func (cp *CPHelper) RunCLI(args ...string) CLIResult {
	cmd := fmt.Sprintf("NEUTREE_LAUNCH_WORK_DIR=%s %s %s",
		cp.composeDir, cp.cliBin, strings.Join(args, " "))
	return cp.RunCmd(cmd)
}

// DeployCLIBinary deploys neutree-cli to {composeDir}/neutree-cli.
// If cli_url is configured, downloads from URL; otherwise copies from BuildCLI().
// Both local and remote modes place the binary at the same path.
func (cp *CPHelper) DeployCLIBinary() {
	if url := profileCPCLIURL(); url != "" {
		By("Downloading CLI binary from: " + url)

		r := cp.RunCmd(fmt.Sprintf("curl -sfL -o %s '%s' && chmod +x %s", cp.cliBin, url, cp.cliBin))
		Expect(r.ExitCode).To(Equal(0),
			"failed to download CLI binary: %s", r.Stderr)

		return
	}

	Expect(cliBinary).NotTo(BeEmpty(),
		"cliBinary not set — BuildCLI() must run before DeployCLIBinary()")

	if cp.local {
		By("Copying CLI binary to: " + cp.cliBin)

		r := cp.RunCmd(fmt.Sprintf("cp %s %s && chmod +x %s", cliBinary, cp.cliBin, cp.cliBin))
		Expect(r.ExitCode).To(Equal(0),
			"failed to copy CLI binary: %s", r.Stderr)

		return
	}

	By("SCP neutree-cli to machine: " + cp.host)

	scpCmd := exec.Command("scp", //nolint:gosec // e2e test helper
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-i", cp.keyFile,
		cliBinary,
		fmt.Sprintf("%s@%s:%s", cp.user, cp.host, cp.cliBin),
	)
	scpCmd.Stdout = GinkgoWriter
	scpCmd.Stderr = GinkgoWriter
	Expect(scpCmd.Run()).To(Succeed(), "failed to scp neutree-cli to machine")
}

// CleanAll runs cleanup --remove-data for both components and removes deploy artifacts.
func (cp *CPHelper) CleanAll() {
	cp.RunCLI("cleanup", "neutree-core", "--force", "--remove-data")
	cp.RunCLI("cleanup", "obs-stack", "--force", "--remove-data")
	cp.RunCmd("rm -rf " + cp.composeDir + "/neutree-core " + cp.composeDir + "/obs-stack")
	cp.RunCmd("docker volume ls -q --filter name=neutree-core | xargs -r docker volume rm 2>/dev/null; " +
		"docker volume ls -q --filter name=obs-stack | xargs -r docker volume rm 2>/dev/null; true")
}

// RemoveImages removes all docker images from rendered compose files.
// Call before offline deploy tests to prevent online image cache pollution.
func (cp *CPHelper) RemoveImages() {
	var images []string

	for _, project := range []string{"neutree-core", "obs-stack"} {
		p := cp.tryParseCompose(project)
		if p == nil {
			continue
		}

		for _, svc := range p.Services {
			if svc.Image != "" {
				images = append(images, svc.Image)
			}
		}
	}

	if len(images) > 0 {
		cp.RunCmd("docker rmi -f " + strings.Join(images, " ") + " 2>/dev/null; true")
	}
}

// DownloadOfflineImages downloads the offline image archive to the machine.
func (cp *CPHelper) DownloadOfflineImages(url, destPath string) {
	By("Downloading offline images to machine: " + url)

	r := cp.RunCmd(fmt.Sprintf("curl -sfL -o %s '%s'", destPath, url))
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

// ContainerRunning checks if a container is running (docker ps).
func (cp *CPHelper) ContainerRunning(name string) bool {
	r := cp.RunCmd(fmt.Sprintf("docker ps --format '{{.Names}}' | grep -qx '%s'", name))
	return r.ExitCode == 0
}

// ContainerExists checks if a container exists in any state (docker ps -a).
func (cp *CPHelper) ContainerExists(name string) bool {
	r := cp.RunCmd(fmt.Sprintf("docker ps -a --format '{{.Names}}' | grep -qx '%s'", name))
	return r.ExitCode == 0
}

// ContainerExitCode returns the exit code of a stopped container, or -1 if not found.
func (cp *CPHelper) ContainerExitCode(name string) int {
	r := cp.RunCmd(fmt.Sprintf("docker inspect --format '{{.State.ExitCode}}' '%s'", name))
	if r.ExitCode != 0 {
		return -1
	}

	code := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(r.Stdout), "%d", &code); err != nil {
		return -1
	}

	return code
}

// VolumeExists checks if a docker volume with the given name exists.
func (cp *CPHelper) VolumeExists(name string) bool {
	r := cp.RunCmd(fmt.Sprintf("docker volume inspect %s", name))
	return r.ExitCode == 0
}

// tryParseCompose is like ParseCompose but returns nil on error instead of failing.
func (cp *CPHelper) tryParseCompose(project string) *composetypes.Project {
	composePath := cp.composeDir + "/" + project + "/docker-compose.yml"
	r := cp.RunCmd("cat " + composePath)

	if r.ExitCode != 0 {
		return nil
	}

	p, err := loader.Load(composetypes.ConfigDetails{
		ConfigFiles: []composetypes.ConfigFile{{
			Filename: composePath,
			Content:  []byte(r.Stdout),
		}},
	}, func(o *loader.Options) {
		o.SetProjectName(project, true)
		o.SkipInterpolation = true
	})
	if err != nil {
		return nil
	}

	return p
}

// ParseCompose reads the rendered docker-compose.yml for a component from the
// remote machine and returns the parsed compose Project. Fails the test on error.
func (cp *CPHelper) ParseCompose(project string) *composetypes.Project {
	p := cp.tryParseCompose(project)
	ExpectWithOffset(1, p).NotTo(BeNil(),
		"failed to parse compose for project %s", project)

	return p
}

// containerName returns the container name for a compose service.
func containerName(project string, svc composetypes.ServiceConfig) string {
	if svc.ContainerName != "" {
		return svc.ContainerName
	}

	return project + "-" + svc.Name + "-1"
}

// VerifyDeployed checks that all containers in the compose project exist and have a healthy state:
// running containers pass, exited containers must have exit code 0.
func (cp *CPHelper) VerifyDeployed(p *composetypes.Project) {
	for _, svc := range p.Services {
		c := containerName(p.Name, svc)
		ExpectWithOffset(1, cp.ContainerExists(c)).To(BeTrue(),
			"container %s should exist", c)

		if !cp.ContainerRunning(c) {
			ExpectWithOffset(1, cp.ContainerExitCode(c)).To(Equal(0),
				"exited container %s should have exit code 0", c)
		}
	}
}

// VerifyCleanup checks that all containers in the compose project are removed.
// If removeData is true, also verifies that all volumes are removed;
// otherwise verifies that volumes are preserved.
func (cp *CPHelper) VerifyCleanup(p *composetypes.Project, removeData bool) {
	for _, svc := range p.Services {
		c := containerName(p.Name, svc)
		ExpectWithOffset(1, cp.ContainerExists(c)).To(BeFalse(),
			"container %s should not exist after cleanup", c)
	}

	for name := range p.Volumes {
		vol := p.Name + "_" + name
		if removeData {
			ExpectWithOffset(1, cp.VolumeExists(vol)).To(BeFalse(),
				"volume %s should be removed after --remove-data", vol)
		} else {
			ExpectWithOffset(1, cp.VolumeExists(vol)).To(BeTrue(),
				"volume %s should be preserved after cleanup", vol)
		}
	}
}

// CanLogin attempts to login as admin via GoTrue password grant.
// Returns true only if the full auth chain (postgres → auth → neutree-api) responds with HTTP 200.
func (cp *CPHelper) CanLogin() bool {
	_, err := loginTestUser(cp.APIURL(), profile.Auth.Email, profile.Auth.Password)
	return err == nil
}

// APIURL returns the external API URL for the control plane machine.
func (cp *CPHelper) APIURL() string {
	return fmt.Sprintf("http://%s:%d", cp.host, cp.apiPort)
}

// MetricsRemoteWriteURL returns the vminsert URL for metrics ingestion.
func (cp *CPHelper) MetricsRemoteWriteURL() string {
	return fmt.Sprintf("http://%s:8480/insert/0/prometheus/", cp.host)
}

// DownloadOldCLI downloads the old version CLI binary to the machine.
func (cp *CPHelper) DownloadOldCLI(url string) {
	oldBin := cp.composeDir + "/neutree-cli-old"

	By("Downloading old CLI binary: " + url)

	r := cp.RunCmd(fmt.Sprintf("curl -sfL -o %s '%s' && chmod +x %s", oldBin, url, oldBin))
	Expect(r.ExitCode).To(Equal(0),
		"failed to download old CLI: %s", r.Stderr)
}

// RunOldCLI executes the old version neutree-cli on the machine.
func (cp *CPHelper) RunOldCLI(args ...string) CLIResult {
	oldBin := cp.composeDir + "/neutree-cli-old"
	cmd := fmt.Sprintf("NEUTREE_LAUNCH_WORK_DIR=%s %s %s",
		cp.composeDir, oldBin, strings.Join(args, " "))

	return cp.RunCmd(cmd)
}

// mirrorRegistryArgs returns --mirror-registry and --registry-project flags from profile.
// Returns nil if mirror registry is not configured.
func mirrorRegistryArgs() []string {
	var args []string

	if mr := profileCPMirrorRegistry(); mr != "" {
		args = append(args, "--mirror-registry", mr)
	}

	if rp := profileCPRegistryProject(); rp != "" {
		args = append(args, "--registry-project", rp)
	}

	return args
}

// canDeploySSHEndpoint checks if the engine version is compatible with SSH clusters.
// SSH clusters on v1.0.0 only support vllm v0.8.5.
func canDeploySSHEndpoint() bool {
	if profileEngineName() != v1.EngineNameVLLM {
		return true
	}

	if profile.Cluster.OldVersion == "v1.0.0" {
		return profileEngineOldVersion() == "v0.8.5"
	}

	return true
}

// canDeployK8sEndpoint checks if the engine version is compatible with K8s clusters.
// K8s clusters require vllm >= v0.11.2.
func canDeployK8sEndpoint() bool {
	if profileEngineName() != v1.EngineNameVLLM {
		return true
	}

	minVersion := semver.MustParse("v0.11.2")
	engineVersion, err := semver.NewVersion(profileEngineOldVersion())

	if err != nil {
		return false
	}

	return !engineVersion.LessThan(minVersion)
}

// mirrorRegistryCombinedArg returns a single --mirror-registry flag with registry/project
// combined, for old CLI versions that don't support --registry-project.
func mirrorRegistryCombinedArg() []string {
	mr := profileCPMirrorRegistry()
	if mr == "" {
		return nil
	}

	if rp := profileCPRegistryProject(); rp != "" {
		return []string{"--mirror-registry", mr + "/" + rp}
	}

	return []string{"--mirror-registry", mr}
}
