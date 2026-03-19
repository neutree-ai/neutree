package e2e

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// --- Engine registry setup/teardown ---

var (
	mirrorRegistryURL string
	registryContainer string
)

// SetupLocalRegistry starts a local registry:2 container for engine import tests.
func SetupLocalRegistry() {
	mirrorRegistryURL, registryContainer = StartLocalRegistry()
}

// TeardownLocalRegistry stops and removes the local registry container.
func TeardownLocalRegistry() {
	if registryContainer != "" {
		StopLocalRegistry(registryContainer)
	}
}

// --- EngineHelper (Page Object for engine CLI subcommands) ---

// EngineHelper encapsulates common parameters for engine CLI operations.
type EngineHelper struct {
	workspace      string
	mirrorRegistry string
}

// EngineH is the package-level instance, initialised in BeforeAll.
var EngineH *EngineHelper

// NewEngineHelper creates an EngineHelper with the test workspace and mirror registry.
func NewEngineHelper(mirrorRegistry string) *EngineHelper {
	return &EngineHelper{
		workspace:      profileWorkspace(),
		mirrorRegistry: mirrorRegistry,
	}
}

// Import imports an engine package.
func (e *EngineHelper) Import(packagePath string, extra ...string) CLIResult {
	args := []string{"import", "engine",
		"-p", packagePath,
		"--workspace", e.workspace,
		"--mirror-registry", e.mirrorRegistry,
		"--registry-username", "e2e",
		"--registry-password", "e2e",
	}
	args = append(args, extra...)
	return RunCLI(args...)
}

// ImportSkipImage imports an engine package with --skip-image-push.
func (e *EngineHelper) ImportSkipImage(packagePath string, extra ...string) CLIResult {
	return e.Import(packagePath, append([]string{"--skip-image-push"}, extra...)...)
}

// ImportManifest imports a standalone manifest file without registry config.
func (e *EngineHelper) ImportManifest(manifestPath string, extra ...string) CLIResult {
	args := []string{"import", "engine",
		"-p", manifestPath,
		"--workspace", e.workspace,
		"--skip-image-push",
	}
	args = append(args, extra...)
	return RunCLI(args...)
}

// Get retrieves engine details as JSON.
func (e *EngineHelper) Get(name string) CLIResult {
	return RunCLI("get", "engine", name, "-w", e.workspace, "-o", "json")
}

// Delete deletes an engine.
func (e *EngineHelper) Delete(name string) CLIResult {
	return RunCLI("delete", "engine", name, "-w", e.workspace, "--force")
}

// RemoveVersion removes a specific version from an engine.
func (e *EngineHelper) RemoveVersion(name, version string, extra ...string) CLIResult {
	args := []string{"engine", "remove-version",
		"--name", name,
		"--version", version,
		"-w", e.workspace,
	}
	args = append(args, extra...)

	return RunCLI(args...)
}

// EnsureDeleted deletes an engine, ignoring errors (for cleanup).
func (e *EngineHelper) EnsureDeleted(name string) {
	e.Delete(name)
}

// --- Engine JSON helpers ---

// engineJSON is a lightweight struct for parsing `get engine -o json` output.
type engineJSON struct {
	Spec struct {
		Versions []struct {
			Version      string                       `json:"version"`
			ValuesSchema map[string]any               `json:"values_schema"`
			DeployTempl  map[string]map[string]string `json:"deploy_template"`
			Images       map[string]struct {
				ImageName string `json:"image_name"`
				Tag       string `json:"tag"`
			} `json:"images"`
			SupportedTasks []string `json:"supported_tasks"`
		} `json:"versions"`
		SupportedTasks []string `json:"supported_tasks"`
	} `json:"spec"`
}

// parseEngineJSON parses the JSON output of `get engine -o json`.
func parseEngineJSON(stdout string) engineJSON {
	var e engineJSON
	ExpectWithOffset(1, json.Unmarshal([]byte(stdout), &e)).To(Succeed())
	return e
}

// --- Test package builder ---

// engineManifest describes a minimal manifest.yaml for test engine packages.
type engineManifest struct {
	Name           string
	Version        string
	Images         map[string][2]string         // accelerator -> [imageName, tag]
	ValuesSchema   map[string]any
	DeployTemplate map[string]map[string]string // clusterType -> mode -> content
	SupportedTasks []string
	RealImages     bool   // if true, create real Docker images via docker import/save
	PackageURL     string // if set, include package_url in metadata (for manifest-only mode)
}

// buildEnginePackage creates a tar.gz engine package in a temp file and returns its path.
func buildEnginePackage(m engineManifest) string {
	// Build manifest YAML as nested maps for json/yaml marshal.
	type imageEntry struct {
		accel    string
		name     string
		tag      string
		tarFile  string
		tarData  []byte
	}

	var images []imageEntry
	for accel, img := range m.Images {
		tarFile := "images/dummy.tar"
		var tarData []byte
		if m.RealImages {
			ref := img[0] + ":" + img[1]
			tarFile = "images/" + accel + ".tar"
			tarData = buildDockerImageTar(ref)
		}
		images = append(images, imageEntry{accel, img[0], img[1], tarFile, tarData})
	}

	imageSpecs := []map[string]string{}
	engineImages := map[string]map[string]string{}
	for _, img := range images {
		imageSpecs = append(imageSpecs, map[string]string{
			"image_name": img.name,
			"tag":        img.tag,
			"image_file": img.tarFile,
		})
		engineImages[img.accel] = map[string]string{
			"image_name": img.name,
			"tag":        img.tag,
		}
	}

	engineVersion := map[string]any{
		"version": m.Version,
		"images":  engineImages,
	}
	if m.ValuesSchema != nil {
		engineVersion["values_schema"] = m.ValuesSchema
	}
	if m.DeployTemplate != nil {
		engineVersion["deploy_template"] = m.DeployTemplate
	}
	if m.SupportedTasks != nil {
		engineVersion["supported_tasks"] = m.SupportedTasks
	}

	manifest := map[string]any{
		"manifest_version": "1.0",
		"metadata":         map[string]any{"version": m.Version},
		"images":           imageSpecs,
		"engines": []map[string]any{
			{
				"name":            m.Name,
				"engine_versions": []any{engineVersion},
			},
		},
	}

	manifestBytes, err := json.Marshal(manifest)
	Expect(err).NotTo(HaveOccurred())

	// Create tar.gz in a temp file.
	tmpFile, err := os.CreateTemp("", "e2e-engine-*.tar.gz")
	Expect(err).NotTo(HaveOccurred())
	defer tmpFile.Close()

	gw := gzip.NewWriter(tmpFile)
	tw := tar.NewWriter(gw)

	// Add manifest.yaml
	addTarFile(tw, "manifest.yaml", manifestBytes)

	// Add image tars
	addTarDir(tw, "images/")
	if m.RealImages {
		seen := map[string]bool{}
		for _, img := range images {
			if seen[img.tarFile] {
				continue
			}
			seen[img.tarFile] = true
			addTarFile(tw, img.tarFile, img.tarData)
		}
	} else {
		addTarFile(tw, "images/dummy.tar", makeDummyTar())
	}

	Expect(tw.Close()).To(Succeed())
	Expect(gw.Close()).To(Succeed())

	return tmpFile.Name()
}

func addTarDir(tw *tar.Writer, name string) {
	Expect(tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     name,
		Mode:     0755,
	})).To(Succeed())
}

func addTarFile(tw *tar.Writer, name string, data []byte) {
	Expect(tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     int64(len(data)),
		Mode:     0644,
	})).To(Succeed())
	_, err := tw.Write(data)
	Expect(err).NotTo(HaveOccurred())
}

// makeDummyTar creates a minimal valid tar archive containing one empty file.
func makeDummyTar() []byte {
	tmpFile, err := os.CreateTemp("", "dummy-*.tar")
	Expect(err).NotTo(HaveOccurred())
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	tw := tar.NewWriter(tmpFile)
	Expect(tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "dummy",
		Size:     0,
		Mode:     0644,
	})).To(Succeed())
	Expect(tw.Close()).To(Succeed())

	data, err := os.ReadFile(tmpFile.Name())
	Expect(err).NotTo(HaveOccurred())
	return data
}

// buildDockerImageTar creates a minimal Docker image tagged as ref and returns
// its `docker save` output. The image is removed from the local daemon afterwards.
func buildDockerImageTar(ref string) []byte {
	// Create an empty tar to feed into `docker import`.
	emptyTar, err := os.CreateTemp("", "empty-*.tar")
	Expect(err).NotTo(HaveOccurred())
	defer os.Remove(emptyTar.Name())

	tw := tar.NewWriter(emptyTar)
	Expect(tw.Close()).To(Succeed())
	emptyTar.Close()

	// docker import <empty.tar> <ref>
	cmd := exec.Command("docker", "import", emptyTar.Name(), ref)
	cmd.Stderr = GinkgoWriter
	out, err := cmd.Output()
	Expect(err).NotTo(HaveOccurred(), "docker import failed: %s", string(out))

	// docker save <ref> → bytes
	saveCmd := exec.Command("docker", "save", ref)
	saveOut, err := saveCmd.Output()
	Expect(err).NotTo(HaveOccurred(), "docker save failed")

	// cleanup: remove the image from local daemon
	exec.Command("docker", "rmi", ref).Run() //nolint:errcheck

	return saveOut
}

// buildEngineManifestFile creates a standalone manifest YAML file and returns its path.
func buildEngineManifestFile(m engineManifest) string {
	engineImages := map[string]map[string]string{}
	for accel, img := range m.Images {
		engineImages[accel] = map[string]string{
			"image_name": img[0],
			"tag":        img[1],
		}
	}

	engineVersion := map[string]any{
		"version": m.Version,
		"images":  engineImages,
	}
	if m.ValuesSchema != nil {
		engineVersion["values_schema"] = m.ValuesSchema
	}
	if m.DeployTemplate != nil {
		engineVersion["deploy_template"] = m.DeployTemplate
	}
	if m.SupportedTasks != nil {
		engineVersion["supported_tasks"] = m.SupportedTasks
	}

	metadata := map[string]any{"version": m.Version}
	if m.PackageURL != "" {
		metadata["package_url"] = m.PackageURL
	}

	manifest := map[string]any{
		"manifest_version": "1.0",
		"metadata":         metadata,
		"engines": []map[string]any{
			{
				"name":            m.Name,
				"engine_versions": []any{engineVersion},
			},
		},
	}

	manifestBytes, err := json.Marshal(manifest)
	Expect(err).NotTo(HaveOccurred())

	tmpFile, err := os.CreateTemp("", "e2e-engine-*.yaml")
	Expect(err).NotTo(HaveOccurred())
	defer tmpFile.Close()

	_, err = tmpFile.Write(manifestBytes)
	Expect(err).NotTo(HaveOccurred())

	return tmpFile.Name()
}

// --- Tests ---

var _ = Describe("Engine", Ordered, func() {

	BeforeAll(func() {
		By("Starting local registry")
		SetupLocalRegistry()
		EngineH = NewEngineHelper(mirrorRegistryURL)
	})

	AfterAll(func() {
		By("Stopping local registry")
		TeardownLocalRegistry()
	})

	// --- Import / Create ---

	Describe("Import", Label("engine", "import"), func() {

		It("should create a new engine via CLI import", Label("C2613227"), func() {
			name := "e2e-engine-create"
			DeferCleanup(EngineH.EnsureDeleted, name)

			pkg := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
				SupportedTasks: []string{"text-generation"},
				RealImages:     true,
			})
			defer os.Remove(pkg)

			r := EngineH.Import(pkg)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "Successfully imported")

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].Version).To(Equal("v1.0.0"))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("nvidia_gpu"))
		})

		It("should add a new engine version via CLI import", Label("C2613216"), func() {
			name := "e2e-engine-newver"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Import v1
			pkg1 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(pkg1)

			r := EngineH.ImportSkipImage(pkg1)
			ExpectSuccess(r)

			// Import v2
			pkg2 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v2.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v2.0.0"},
				},
			})
			defer os.Remove(pkg2)

			r = EngineH.ImportSkipImage(pkg2)
			ExpectSuccess(r)

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(2))

			versions := []string{e.Spec.Versions[0].Version, e.Spec.Versions[1].Version}
			Expect(versions).To(ConsistOf("v1.0.0", "v2.0.0"))
		})

		It("should add accelerator architecture to existing version via CLI import", Label("C2613217"), func() {
			name := "e2e-engine-accel"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Import with nvidia_gpu only
			pkg1 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(pkg1)

			r := EngineH.ImportSkipImage(pkg1)
			ExpectSuccess(r)

			// Import with amd_gpu for same version, --force to merge
			pkg2 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"amd_gpu": {"e2e/engine-rocm", "v1.0.0"},
				},
			})
			defer os.Remove(pkg2)

			r = EngineH.ImportSkipImage(pkg2, "--force")
			ExpectSuccess(r)

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("nvidia_gpu"))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("amd_gpu"))
		})

		It("should update K8s deployment config via CLI import", Label("C2613218"), func() {
			name := "e2e-engine-deploy"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Import without deploy template
			pkg1 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(pkg1)

			r := EngineH.ImportSkipImage(pkg1)
			ExpectSuccess(r)

			// Import with deploy template, --force to merge
			pkg2 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
				DeployTemplate: map[string]map[string]string{
					"kubernetes": {"default": "dGVzdC10ZW1wbGF0ZQ=="}, // base64("test-template")
				},
			})
			defer os.Remove(pkg2)

			r = EngineH.ImportSkipImage(pkg2, "--force")
			ExpectSuccess(r)

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].DeployTempl).To(HaveKey("kubernetes"))
			Expect(e.Spec.Versions[0].DeployTempl["kubernetes"]).To(HaveKey("default"))
		})

		It("should create engine from standalone manifest YAML", Label("manifest"), func() {
			name := "e2e-engine-manifest-create"
			DeferCleanup(EngineH.EnsureDeleted, name)

			manifest := buildEngineManifestFile(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
				SupportedTasks: []string{"text-generation"},
			})
			defer os.Remove(manifest)

			r := EngineH.ImportManifest(manifest)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "Successfully imported")

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].Version).To(Equal("v1.0.0"))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("nvidia_gpu"))
			Expect(e.Spec.Versions[0].SupportedTasks).To(ContainElement("text-generation"))
		})

		It("should add version to existing engine via manifest import", Label("manifest"), func() {
			name := "e2e-engine-manifest-addver"
			DeferCleanup(EngineH.EnsureDeleted, name)

			m1 := buildEngineManifestFile(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(m1)

			r := EngineH.ImportManifest(m1)
			ExpectSuccess(r)

			m2 := buildEngineManifestFile(engineManifest{
				Name:    name,
				Version: "v2.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v2.0.0"},
				},
			})
			defer os.Remove(m2)

			r = EngineH.ImportManifest(m2)
			ExpectSuccess(r)

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(2))

			versions := []string{e.Spec.Versions[0].Version, e.Spec.Versions[1].Version}
			Expect(versions).To(ConsistOf("v1.0.0", "v2.0.0"))
		})

		It("should merge accelerator via manifest import with --force", Label("manifest"), func() {
			name := "e2e-engine-manifest-accel"
			DeferCleanup(EngineH.EnsureDeleted, name)

			m1 := buildEngineManifestFile(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(m1)

			r := EngineH.ImportManifest(m1)
			ExpectSuccess(r)

			m2 := buildEngineManifestFile(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"amd_gpu": {"e2e/engine-rocm", "v1.0.0"},
				},
			})
			defer os.Remove(m2)

			r = EngineH.ImportManifest(m2, "--force")
			ExpectSuccess(r)

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("nvidia_gpu"))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("amd_gpu"))
		})

		It("should validate standalone manifest YAML via validate command", Label("manifest"), func() {
			manifest := buildEngineManifestFile(engineManifest{
				Name:    "e2e-engine-validate-manifest",
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(manifest)

			r := RunCLI("import", "validate", "-p", manifest)
			ExpectSuccess(r)
		})

		It("should download and import images via manifest with package_url", Label("manifest", "docker"), func() {

			name := "e2e-engine-manifest-pkgurl"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Build a real engine package archive to serve via HTTP
			pkg := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda-pkgurl", "v1.0.0"},
				},
				RealImages: true,
			})
			defer os.Remove(pkg)

			// Start a local HTTP file server to serve the package
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			Expect(err).NotTo(HaveOccurred())

			pkgDir := filepath.Dir(pkg)
			pkgFile := filepath.Base(pkg)
			server := &http.Server{Handler: http.FileServer(http.Dir(pkgDir))}

			go server.Serve(listener) //nolint:errcheck
			defer server.Close()

			packageURL := fmt.Sprintf("http://%s/%s", listener.Addr().String(), pkgFile)

			// Create manifest with package_url pointing to local server
			manifest := buildEngineManifestFile(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda-pkgurl", "v1.0.0"},
				},
				PackageURL: packageURL,
			})
			defer os.Remove(manifest)

			r := EngineH.Import(manifest)
			ExpectSuccess(r)
			ExpectStdoutContains(r, "Successfully imported")

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].Images).To(HaveKey("nvidia_gpu"))
		})

		It("should update values schema via CLI import", Label("C2613219"), func() {
			name := "e2e-engine-schema"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Import without schema
			pkg1 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
			})
			defer os.Remove(pkg1)

			r := EngineH.ImportSkipImage(pkg1)
			ExpectSuccess(r)

			// Import with values schema, --force to merge
			pkg2 := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images: map[string][2]string{
					"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"},
				},
				ValuesSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"gpu_memory_utilization": map[string]any{
							"type":    "number",
							"default": 0.9,
						},
					},
				},
			})
			defer os.Remove(pkg2)

			r = EngineH.ImportSkipImage(pkg2, "--force")
			ExpectSuccess(r)

			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].ValuesSchema).To(HaveKey("properties"))
		})
	})

	// --- Remove Version ---

	Describe("RemoveVersion", Label("engine", "remove-version"), func() {

		It("should remove a version from a multi-version engine", func() {
			name := "e2e-engine-rmver"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Import v1 and v2
			pkg1 := buildEnginePackage(engineManifest{
				Name:           name,
				Version:        "v1.0.0",
				Images:         map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"}},
				SupportedTasks: []string{"text-generation"},
			})
			defer os.Remove(pkg1)

			r := EngineH.ImportSkipImage(pkg1)
			ExpectSuccess(r)

			pkg2 := buildEnginePackage(engineManifest{
				Name:           name,
				Version:        "v2.0.0",
				Images:         map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v2.0.0"}},
				SupportedTasks: []string{"text-generation", "text-embedding"},
			})
			defer os.Remove(pkg2)

			r = EngineH.ImportSkipImage(pkg2)
			ExpectSuccess(r)

			// Verify 2 versions exist
			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(2))

			// Remove v1.0.0
			r = EngineH.RemoveVersion(name, "v1.0.0")
			ExpectSuccess(r)
			ExpectStdoutContains(r, "Successfully removed")

			// Verify only v2.0.0 remains
			r = EngineH.Get(name)
			ExpectSuccess(r)
			e = parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.Versions[0].Version).To(Equal("v2.0.0"))
		})

		It("should reject removing the last version without --force", func() {
			name := "e2e-engine-rmver-last"
			DeferCleanup(EngineH.EnsureDeleted, name)

			pkg := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images:  map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"}},
			})
			defer os.Remove(pkg)

			r := EngineH.ImportSkipImage(pkg)
			ExpectSuccess(r)

			// Try to remove the only version without --force
			r = EngineH.RemoveVersion(name, "v1.0.0")
			ExpectFailed(r)
		})

		It("should delete engine when force-removing the last version", func() {
			name := "e2e-engine-rmver-force"
			DeferCleanup(EngineH.EnsureDeleted, name)

			pkg := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images:  map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"}},
			})
			defer os.Remove(pkg)

			r := EngineH.ImportSkipImage(pkg)
			ExpectSuccess(r)

			// Force remove the last version — should delete the engine
			r = EngineH.RemoveVersion(name, "v1.0.0", "--force")
			ExpectSuccess(r)
			ExpectStdoutContains(r, "deleted engine")

			// Verify engine no longer exists
			r = EngineH.Get(name)
			ExpectFailed(r)
		})

		It("should fail when removing a non-existent version", func() {
			name := "e2e-engine-rmver-noexist"
			DeferCleanup(EngineH.EnsureDeleted, name)

			pkg := buildEnginePackage(engineManifest{
				Name:    name,
				Version: "v1.0.0",
				Images:  map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"}},
			})
			defer os.Remove(pkg)

			r := EngineH.ImportSkipImage(pkg)
			ExpectSuccess(r)

			r = EngineH.RemoveVersion(name, "v9.9.9")
			ExpectFailed(r)
		})

		It("should recalculate SupportedTasks after version removal", func() {
			name := "e2e-engine-rmver-tasks"
			DeferCleanup(EngineH.EnsureDeleted, name)

			// Import v1 with text-generation
			pkg1 := buildEnginePackage(engineManifest{
				Name:           name,
				Version:        "v1.0.0",
				Images:         map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v1.0.0"}},
				SupportedTasks: []string{"text-generation"},
			})
			defer os.Remove(pkg1)

			r := EngineH.ImportSkipImage(pkg1)
			ExpectSuccess(r)

			// Import v2 with text-embedding
			pkg2 := buildEnginePackage(engineManifest{
				Name:           name,
				Version:        "v2.0.0",
				Images:         map[string][2]string{"nvidia_gpu": {"e2e/engine-cuda", "v2.0.0"}},
				SupportedTasks: []string{"text-embedding"},
			})
			defer os.Remove(pkg2)

			r = EngineH.ImportSkipImage(pkg2)
			ExpectSuccess(r)

			// Remove v2.0.0 (which has the unique text-embedding task)
			r = EngineH.RemoveVersion(name, "v2.0.0")
			ExpectSuccess(r)

			// Verify SupportedTasks no longer contains text-embedding
			r = EngineH.Get(name)
			ExpectSuccess(r)
			e := parseEngineJSON(r.Stdout)
			Expect(e.Spec.Versions).To(HaveLen(1))
			Expect(e.Spec.SupportedTasks).To(ContainElement("text-generation"))
			Expect(e.Spec.SupportedTasks).NotTo(ContainElement("text-embedding"))
		})
	})
})

