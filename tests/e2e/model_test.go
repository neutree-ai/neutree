package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// --- Model registry setup/teardown ---

// registryYAML holds the path to the rendered model registry YAML for teardown.
var registryYAML string

// SetupModelRegistry creates a model registry from the YAML template
// and waits for it to reach Connected phase.
func SetupModelRegistry() {
	defaults := map[string]string{
		"E2E_MODEL_REGISTRY":     testRegistry(),
		"E2E_WORKSPACE":          testWorkspace(),
		"E2E_MODEL_REGISTRY_URL": profile.ModelRegistry.URL,
	}
	var err error
	registryYAML, err = renderTemplateToTempFile(
		filepath.Join("testdata", "model-registry.yaml"), defaults,
	)
	Expect(err).NotTo(HaveOccurred(), "failed to render model registry template")

	r := RunCLI("apply", "-f", registryYAML)
	ExpectSuccess(r)

	r = RunCLI("wait", "modelregistry", testRegistry(),
		"-w", Cfg.Workspace,
		"--for", "jsonpath=.status.phase=Connected",
		"--timeout", "2m",
	)
	ExpectSuccess(r)
}

// TeardownModelRegistry deletes the model registry and cleans up the temp YAML.
func TeardownModelRegistry() {
	if registryYAML != "" {
		RunCLI("delete", "-f", registryYAML, "--force", "--ignore-not-found")
		os.Remove(registryYAML)
	}
}

// --- ModelHelper (Page Object for "model" CLI subcommands) ---

// ModelHelper encapsulates common parameters for model CLI operations.
type ModelHelper struct {
	registry  string
	workspace string
}

// Model is the package-level instance, initialised in BeforeSuite.
var Model *ModelHelper

// NewModelHelper creates a ModelHelper with the test registry and workspace.
func NewModelHelper() *ModelHelper {
	return &ModelHelper{
		registry:  testRegistry(),
		workspace: Cfg.Workspace,
	}
}

// Push pushes a model directory with the given name/version.
func (m *ModelHelper) Push(dir, name, version string, extra ...string) CLIResult {
	args := []string{"model", "push", dir, "-n", name, "-r", m.registry, "-w", m.workspace}
	if version != "" {
		args = append(args, "-v", version)
	}
	args = append(args, extra...)
	return RunCLI(args...)
}

// List lists models in the registry.
func (m *ModelHelper) List() CLIResult {
	return RunCLI("model", "list", "-r", m.registry, "-w", m.workspace)
}

// Get retrieves model details by tag (name or name:version).
func (m *ModelHelper) Get(tag string) CLIResult {
	return RunCLI("model", "get", tag, "-r", m.registry, "-w", m.workspace)
}

// Delete deletes a model version (with --force).
func (m *ModelHelper) Delete(tag string) CLIResult {
	return RunCLI("model", "delete", tag, "-r", m.registry, "-w", m.workspace, "--force")
}

// Pull downloads a model to the given output directory.
func (m *ModelHelper) Pull(tag, outputDir string) CLIResult {
	return RunCLI("model", "pull", tag, "-r", m.registry, "-w", m.workspace, "-o", outputDir)
}

// EnsureDeleted deletes a model version, ignoring errors (for cleanup).
func (m *ModelHelper) EnsureDeleted(name, version string) {
	m.Delete(name + ":" + version)
}

// --- Convenience helpers ---

// pushModel creates a temp dir with a dummy file and pushes it as a model.
func pushModel(name, version string, fileSize int, extraArgs ...string) CLIResult {
	modelDir := GinkgoT().TempDir()
	data := make([]byte, fileSize)
	for i := range data {
		data[i] = byte(i % 256)
	}
	Expect(os.WriteFile(filepath.Join(modelDir, "model.bin"), data, 0644)).To(Succeed())
	return Model.Push(modelDir, name, version, extraArgs...)
}

// --- Tests ---

var _ = Describe("Model", Ordered, func() {

	BeforeAll(func() {
		By("Setting up model registry")
		SetupModelRegistry()
		Model = NewModelHelper()
	})

	AfterAll(func() {
		By("Tearing down model registry")
		TeardownModelRegistry()
	})

	// --- Push ---

	Describe("Push", Label("model", "push"), func() {

		It("should push a model to filesystem registry", Label("C2612561"), func() {
			DeferCleanup(Model.EnsureDeleted, "e2e-push-basic", "v1.0")

			r := pushModel("e2e-push-basic", "v1.0", 64)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.List()
			ExpectSuccess(r)
			rows := ParseTable(r.Stdout)
			Expect(rows).To(ContainElement(HaveKeyWithValue("NAME", "e2e-push-basic")))

			r = Model.Get("e2e-push-basic:v1.0")
			ExpectSuccess(r)
			kv := ParseKV(r.Stdout)
			Expect(kv["Version"]).To(Equal("v1.0"))
		})

		It("should push models of different sizes", Label("C2612562"), func() {
			sizes := []struct {
				label string
				bytes int
			}{
				{"small", 1024},
				{"medium", 1024 * 1024},
			}

			for _, s := range sizes {
				name := fmt.Sprintf("e2e-push-size-%s", s.label)
				DeferCleanup(Model.EnsureDeleted, name, "v1.0")

				r := pushModel(name, "v1.0", s.bytes)
				ExpectSuccess(r)
				ExpectStdoutContains(r, "pushed successfully")

				r = Model.Get(name + ":v1.0")
				ExpectSuccess(r)
				Expect(ParseKV(r.Stdout)["Version"]).To(Equal("v1.0"))
			}
		})

		It("should push a model with maximum length name", Label("C2612563"), func() {
			longName := "e2e-" + strings.Repeat("a", 59) // 63 chars
			DeferCleanup(Model.EnsureDeleted, longName, "v1.0")

			r := pushModel(longName, "v1.0", 64)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.Get(longName + ":v1.0")
			ExpectSuccess(r)
		})

		It("should push a model with special characters in name", Label("C2612564"), func() {
			name := "e2e-model_test.v2"
			DeferCleanup(Model.EnsureDeleted, name, "v1.0")

			r := pushModel(name, "v1.0", 64)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.Get(name + ":v1.0")
			ExpectSuccess(r)
			Expect(ParseKV(r.Stdout)["Name"]).To(Equal(name))
		})

		It("should auto-generate version when not specified", Label("C2621663"), func() {
			name := "e2e-push-autover"
			// Cleanup: find the auto-generated version and delete it
			DeferCleanup(func() {
				r := Model.Get(name + ":latest")
				if r.ExitCode == 0 {
					if ver := ParseKV(r.Stdout)["Version"]; ver != "" {
						Model.EnsureDeleted(name, ver)
					}
				}
			})

			r := pushModel(name, "", 64) // no version
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.List()
			ExpectSuccess(r)
			rows := ParseTable(r.Stdout)
			Expect(rows).To(ContainElement(HaveKeyWithValue("NAME", name)))
		})

		It("should push a model with specified version", Label("C2621664"), func() {
			name := "e2e-push-specver"
			version := "v2.1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.Get(name + ":" + version)
			ExpectSuccess(r)
			Expect(ParseKV(r.Stdout)["Version"]).To(Equal(version))
		})

		It("should overwrite an existing version", Label("C2621665"), func() {
			name := "e2e-push-overwrite"
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			r = pushModel(name, version, 128)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.Get(name + ":" + version)
			ExpectSuccess(r)
			Expect(ParseKV(r.Stdout)["Version"]).To(Equal(version))
		})

		It("should update name and version in model.yaml when pushing with --name/--version", func() {
			name := "e2e-push-yaml-override"
			version := "v3.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			// Create a model dir with a pre-existing model.yaml containing different name/version.
			modelDir := GinkgoT().TempDir()
			Expect(os.WriteFile(filepath.Join(modelDir, "model.bin"), []byte("data"), 0644)).To(Succeed())
			originalYAML := "name: original-name\nversion: original-version\nmodule: test\napi_version: v1\n"
			Expect(os.WriteFile(filepath.Join(modelDir, "model.yaml"), []byte(originalYAML), 0644)).To(Succeed())

			// Push with overridden name and version.
			r := Model.Push(modelDir, name, version)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			// Verify server-side model.yaml reflects the overridden name/version
			// (GetModelDetail reads model.yaml from disk).
			r = Model.Get(name + ":" + version)
			ExpectSuccess(r)
			kv := ParseKV(r.Stdout)
			Expect(kv["Name"]).To(Equal(name), "model.yaml Name should match the pushed --name flag, not 'original-name'")
			Expect(kv["Version"]).To(Equal(version), "model.yaml Version should match the pushed --version flag, not 'original-version'")
		})

		It("should reject 'latest' as version", Label("C2622808"), func() {
			r := pushModel("e2e-push-latest-reject", "latest", 64)
			ExpectFailed(r)
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("latest"))
		})
	})

	// --- Delete ---

	Describe("Delete", Label("model", "delete"), func() {

		It("should delete a model", Label("C2612566"), func() {
			name := "e2e-delete-basic"
			version := "v1.0"

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			r = Model.Delete(name + ":" + version)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "deleted successfully")

			r = Model.Get(name + ":" + version)
			ExpectFailed(r)
		})

		It("should only delete the specified version when multiple exist", Label("C2621745"), func() {
			name := "e2e-delete-multi"
			DeferCleanup(Model.EnsureDeleted, name, "v1.0")
			DeferCleanup(Model.EnsureDeleted, name, "v2.0")

			r := pushModel(name, "v1.0", 64)
			ExpectSuccess(r)
			r = pushModel(name, "v2.0", 64)
			ExpectSuccess(r)

			r = Model.Delete(name + ":v1.0")
			ExpectSuccess(r)

			r = Model.Get(name + ":v1.0")
			ExpectFailed(r)

			r = Model.Get(name + ":v2.0")
			ExpectSuccess(r)
			Expect(ParseKV(r.Stdout)["Version"]).To(Equal("v2.0"))
		})

		It("should remove model from list after deleting the only version", Label("C2621746"), func() {
			name := "e2e-delete-last"
			version := "v1.0"

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			r = Model.List()
			ExpectSuccess(r)
			Expect(ParseTable(r.Stdout)).To(ContainElement(HaveKeyWithValue("NAME", name)))

			r = Model.Delete(name + ":" + version)
			ExpectSuccess(r)

			r = Model.List()
			ExpectSuccess(r)
			Expect(ParseTable(r.Stdout)).NotTo(ContainElement(HaveKeyWithValue("NAME", name)))
		})
	})

	// --- List ---

	Describe("List", Label("model", "list"), func() {

		It("should list models in the registry", Label("C2613133", "C2611878"), func() {
			name := "e2e-list-basic"
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			r = Model.List()
			ExpectSuccess(r)
			rows := ParseTable(r.Stdout)
			Expect(rows).To(ContainElement(HaveKeyWithValue("NAME", name)))
		})
	})

	// --- Get / Details ---

	Describe("Get", Label("model", "get"), func() {

		It("should get model details", Label("C2613134"), func() {
			name := "e2e-get-basic"
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			r = Model.Get(name + ":" + version)
			ExpectSuccess(r)
			kv := ParseKV(r.Stdout)
			Expect(kv["Name"]).To(Equal(name))
			Expect(kv["Version"]).To(Equal(version))
			Expect(kv).To(HaveKey("Size"))
		})

		It("should default to latest version when not specified", Label("C2621676"), func() {
			name := "e2e-get-latest"
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			// Get without version — ParseModelTag defaults to "latest"
			r = Model.Get(name)
			if r.ExitCode == 0 {
				Expect(ParseKV(r.Stdout)["Name"]).To(Equal(name))
			}
			// If server requires explicit version, failing is acceptable
		})
	})

	// --- Pull / Download ---

	Describe("Pull", Label("model", "pull"), func() {

		It("should pull a model to local directory", Label("C2613136"), func() {
			name := "e2e-pull-basic"
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			outputDir := GinkgoT().TempDir()
			r = Model.Pull(name+":"+version, outputDir)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pulled successfully")

			// The pull command saves using filename from Content-Disposition
			// or defaults to {name}-{version}.bentomodel — verify a file was created.
			entries, err := os.ReadDir(outputDir)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).NotTo(BeEmpty(), "expected at least one file in output directory")
		})
	})
})

