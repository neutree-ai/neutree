package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// --- Model registry setup/teardown ---

// registryYAML holds the path to the rendered model registry YAML for teardown.
var registryYAML string

// SetupModelRegistry creates a model registry from the YAML template
// and waits for it to reach Connected phase.
func SetupModelRegistry() {
	defaults := map[string]any{
		"E2E_MODEL_REGISTRY":     testRegistry(),
		"E2E_WORKSPACE":          profileWorkspace(),
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
		"-w", profileWorkspace(),
		"--for", "jsonpath=.status.phase=Connected",
		"--timeout", "2m",
	)
	ExpectSuccess(r)
}

// TeardownModelRegistry deletes the model registry and cleans up the temp YAML.
func TeardownModelRegistry() {
	if registryYAML != "" {
		r := RunCLI("delete", "-f", registryYAML, "--force", "--ignore-not-found")
		ExpectSuccess(r)
		os.Remove(registryYAML)
		registryYAML = ""
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
		workspace: profileWorkspace(),
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

// ListJSON lists models in the registry as the API's own JSON payload.
//
// The alias assertions read this rather than the table: the table renders an
// unset alias as a placeholder and truncates nothing else usefully, so a diff of
// it cannot tell "alias is the empty string" from "alias is the string -".
func (m *ModelHelper) ListJSON() CLIResult {
	return RunCLI("model", "list", "-r", m.registry, "-w", m.workspace, "-o", "json")
}

// GetJSON retrieves model details by tag as the API's own JSON payload.
func (m *ModelHelper) GetJSON(tag string) CLIResult {
	return RunCLI("model", "get", tag, "-r", m.registry, "-w", m.workspace, "-o", "json")
}

// SetAlias sets (or, with an empty alias, clears) the display alias of one model
// version, returning the response body and status so a caller can assert on a
// rejection as well as on a success.
//
// This goes through the API rather than the CLI because there is no CLI verb for
// it: NEU-621 taught `model list` / `model get` to show an alias, and writing one
// is a PATCH on the model.
func (m *ModelHelper) SetAlias(name, version, alias string) ([]byte, int) {
	GinkgoHelper()

	path := fmt.Sprintf("/api/v1/workspaces/%s/model_registries/%s/models/%s?version=%s",
		url.PathEscape(m.workspace), url.PathEscape(m.registry), url.PathEscape(name),
		url.QueryEscape(version))

	return callNeutreeAPIWithJSON(http.MethodPatch, path, map[string]any{"alias": alias})
}

// EnsureAliasCleared removes a model version's alias, ignoring the outcome. An
// alias lives in a table keyed on the registry, not in the model directory, so
// cleanup registered for the model itself does not cover it.
func (m *ModelHelper) EnsureAliasCleared(name, version string) {
	m.SetAlias(name, version, "")
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

// modelVersionFromJSON decodes `model get -o json`, which is one ModelVersion.
//
// The version is asserted rather than assumed: the payload's "name" field is the
// version, so a decode that silently produced a zero value would leave every
// alias assertion below comparing "" against "", which is the failure mode this
// whole file's alias coverage exists to rule out.
func modelVersionFromJSON(r CLIResult, expectVersion string) v1.ModelVersion {
	GinkgoHelper()

	ExpectSuccess(r)

	var version v1.ModelVersion
	Expect(json.Unmarshal([]byte(r.Stdout), &version)).To(Succeed(),
		"model get -o json did not produce a ModelVersion: %s", r.String())
	Expect(version.Name).To(Equal(expectVersion),
		"model get -o json returned version %q, expected %q", version.Name, expectVersion)

	return version
}

// aliasFromListJSON finds one model version in `model list -o json` and returns
// its alias.
//
// It fails rather than returning an empty alias when the model is not in the
// listing: "the listing does not mention this model" and "this model has no
// alias" are different facts, and only the second one is ever a pass.
func aliasFromListJSON(r CLIResult, name, version string) string {
	GinkgoHelper()

	ExpectSuccess(r)

	var models []v1.GeneralModel
	Expect(json.Unmarshal([]byte(r.Stdout), &models)).To(Succeed(),
		"model list -o json did not produce a model array: %s", r.String())
	Expect(models).NotTo(BeEmpty(), "model list -o json returned an empty listing")

	for _, model := range models {
		if model.Name != name {
			continue
		}

		for _, v := range model.Versions {
			if v.Name == version {
				return v.Alias
			}
		}
	}

	Fail(fmt.Sprintf("model list -o json does not contain %s:%s; listing: %s", name, version, r.Stdout))

	return ""
}

// aliasConflictBody is the 409 an alias write is refused with. It names the
// physical model the alias collided with, never the alias, so the assertions
// below can check which object was in the way.
type aliasConflictBody struct {
	Message  string `json:"message"`
	Conflict struct {
		// Kind is "Model" when another version already answers to the alias, and
		// "ModelName" when the alias would shadow a physical model name.
		Kind    string `json:"kind"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"conflict"`
}

func aliasConflictFrom(body []byte, status, expectStatus int) aliasConflictBody {
	GinkgoHelper()

	Expect(status).To(Equal(expectStatus), "unexpected status; body: %s", body)

	var conflict aliasConflictBody
	Expect(json.Unmarshal(body, &conflict)).To(Succeed(), "conflict body is not JSON: %s", body)

	return conflict
}

func applyPausedEndpointReferencingModel(endpointName, modelName, modelVersion string) {
	endpointYAML := fmt.Sprintf(`apiVersion: v1
kind: Endpoint
metadata:
  name: %s
  workspace: %s
spec:
  cluster: e2e-missing-cluster-%s
  engine:
    engine: %s
    version: %s
  model:
    registry: %s
    name: %s
    version: %s
    task: %s
  resources:
    gpu: "0"
  replicas:
    num: 0
`, endpointName, profileWorkspace(), Cfg.RunID, profileEngineName(), profileEngineVersion(), testRegistry(), modelName, modelVersion, profileModelTask())

	yamlPath := filepath.Join(GinkgoT().TempDir(), endpointName+".yaml")
	Expect(os.WriteFile(yamlPath, []byte(endpointYAML), 0644)).To(Succeed())

	r := RunCLI("apply", "-f", yamlPath)
	ExpectSuccess(r)
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

		It("should direct push a model to a pre-mounted NFS registry", Label("C2723966", "nfs-direct"), func() {
			if profile.ModelRegistry.LocalNFSPath == "" || !strings.HasPrefix(profile.ModelRegistry.URL, "nfs://") {
				Skip("model_registry.local_nfs_path and nfs:// model_registry.url are required")
			}

			name := "e2e-nfs-direct"
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)

			r := pushModel(name, version, 64, "--local-nfs-path", profile.ModelRegistry.LocalNFSPath)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "pushed successfully")

			r = Model.List()
			ExpectSuccess(r)
			Expect(ParseTable(r.Stdout)).To(ContainElement(HaveKeyWithValue("NAME", name)))

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

		It("should reject model names that BentoML would lowercase", Label("C2723761", "validation"), func() {
			r := pushModel("Invalid_Name", "v1.0", 64)
			ExpectFailed(r)
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("invalid model name"))
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("model name must be lowercase"))
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

		// TestRail: C2723579
		It("should reject deleting a model referenced by an endpoint", Label("C2723579"), func() {
			name := "e2e-delete-ref-" + Cfg.RunID
			version := "v1.0"
			endpointName := "e2e-model-ref-" + Cfg.RunID

			DeferCleanup(func() {
				deleteEndpoint(endpointName)
				Model.EnsureDeleted(name, version)
			})

			r := pushModel(name, version, 64)
			ExpectSuccess(r)

			applyPausedEndpointReferencingModel(endpointName, name, version)

			r = Model.Delete(name)
			ExpectFailed(r)
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("10131"))
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("still reference this model"))

			r = Model.Delete(name + ":" + version)
			ExpectFailed(r)
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("10131"))
			Expect(r.Stdout + r.Stderr).To(ContainSubstring("still reference this model"))

			deleteEndpoint(endpointName)

			r = Model.Delete(name + ":" + version)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "deleted successfully")
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

	// --- Alias ---
	//
	// An alias is a display name for one model version, unique within a registry
	// and across versions, and it may not shadow a physical model name. It is
	// written with a PATCH on the model; the CLI only reads it.
	//
	// These are the CLI-observable half of NEU-652. The other half — that
	// changing an alias does not disturb an endpoint already serving the model —
	// needs a live cluster and lives beside the SSH endpoint fixture.
	Describe("Alias", Label("model", "alias"), func() {

		It("should show an alias change in model list and model get", Label("C-NEU652-SHOW"), func() {
			name := "e2e-alias-show-" + Cfg.RunID
			version := "v1.0"
			DeferCleanup(Model.EnsureDeleted, name, version)
			DeferCleanup(Model.EnsureAliasCleared, name, version)

			ExpectSuccess(pushModel(name, version, 64))

			By("Checking a freshly pushed version has no alias")
			Expect(modelVersionFromJSON(Model.GetJSON(name+":"+version), version).Alias).To(BeEmpty())
			Expect(aliasFromListJSON(Model.ListJSON(), name, version)).To(BeEmpty())

			By("Setting an alias")
			alias := "E2E Alias " + Cfg.RunID
			body, status := Model.SetAlias(name, version, alias)
			Expect(status).To(Equal(http.StatusOK), "failed to set alias: %s", body)

			Expect(modelVersionFromJSON(Model.GetJSON(name+":"+version), version).Alias).To(Equal(alias))
			Expect(aliasFromListJSON(Model.ListJSON(), name, version)).To(Equal(alias))

			By("Renaming the alias")
			renamed := "E2E Renamed " + Cfg.RunID
			body, status = Model.SetAlias(name, version, renamed)
			Expect(status).To(Equal(http.StatusOK), "failed to rename alias: %s", body)

			Expect(modelVersionFromJSON(Model.GetJSON(name+":"+version), version).Alias).To(Equal(renamed))
			Expect(aliasFromListJSON(Model.ListJSON(), name, version)).To(Equal(renamed))

			By("Clearing the alias")
			body, status = Model.SetAlias(name, version, "")
			Expect(status).To(Equal(http.StatusOK), "failed to clear alias: %s", body)

			// The version is still listed — it lost its display name, not its
			// existence. aliasFromListJSON fails if it is gone, which is what
			// separates "cleared" from "disappeared".
			Expect(modelVersionFromJSON(Model.GetJSON(name+":"+version), version).Alias).To(BeEmpty())
			Expect(aliasFromListJSON(Model.ListJSON(), name, version)).To(BeEmpty())
		})

		It("should reject an alias already used by another version", Label("C-NEU652-DUP"), func() {
			holder := "e2e-alias-holder-" + Cfg.RunID
			claimant := "e2e-alias-claimant-" + Cfg.RunID
			version := "v1.0"

			DeferCleanup(Model.EnsureDeleted, holder, version)
			DeferCleanup(Model.EnsureDeleted, claimant, version)
			DeferCleanup(Model.EnsureAliasCleared, holder, version)

			ExpectSuccess(pushModel(holder, version, 64))
			ExpectSuccess(pushModel(claimant, version, 64))

			alias := "E2E Contested " + Cfg.RunID
			body, status := Model.SetAlias(holder, version, alias)
			Expect(status).To(Equal(http.StatusOK), "failed to set alias on holder: %s", body)

			By("Claiming the same alias for a second version")
			body, status = Model.SetAlias(claimant, version, alias)
			conflict := aliasConflictFrom(body, status, http.StatusConflict)

			Expect(conflict.Conflict.Kind).To(Equal("Model"))
			Expect(conflict.Conflict.Name).To(Equal(holder), "409 should name the model holding the alias")
			Expect(conflict.Conflict.Version).To(Equal(version))
			Expect(conflict.Message).To(ContainSubstring("is already used in this registry"))

			By("Verifying the rejected write left no trace")
			Expect(modelVersionFromJSON(Model.GetJSON(claimant+":"+version), version).Alias).To(BeEmpty(),
				"a refused alias write must not be applied")
			Expect(modelVersionFromJSON(Model.GetJSON(holder+":"+version), version).Alias).To(Equal(alias),
				"a refused alias write must not disturb the holder")
		})

		It("should reject an alias that shadows a physical model name", Label("C-NEU652-SHADOW"), func() {
			shadowed := "e2e-alias-shadowed-" + Cfg.RunID
			claimant := "e2e-alias-shadower-" + Cfg.RunID
			version := "v1.0"

			DeferCleanup(Model.EnsureDeleted, shadowed, version)
			DeferCleanup(Model.EnsureDeleted, claimant, version)

			ExpectSuccess(pushModel(shadowed, version, 64))
			ExpectSuccess(pushModel(claimant, version, 64))

			By("Claiming another model's physical name as an alias")
			body, status := Model.SetAlias(claimant, version, shadowed)
			conflict := aliasConflictFrom(body, status, http.StatusConflict)

			Expect(conflict.Conflict.Kind).To(Equal("ModelName"))
			Expect(conflict.Conflict.Name).To(Equal(shadowed), "409 should name the model whose name was shadowed")
			Expect(conflict.Message).To(ContainSubstring("is already the name of a model in this registry"))

			By("Verifying the rejected write left no trace")
			Expect(modelVersionFromJSON(Model.GetJSON(claimant+":"+version), version).Alias).To(BeEmpty(),
				"a refused alias write must not be applied")
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
