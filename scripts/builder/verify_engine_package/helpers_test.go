package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cli/packageimport"
)

func TestSplitTasks(t *testing.T) {
	if tasks := splitTasks(""); tasks != nil {
		t.Fatalf("splitTasks(\"\") = %#v, want nil", tasks)
	}
	want := []string{"text-generation", "text-embedding"}
	if tasks := splitTasks(" text-generation, text-embedding "); !sameStrings(tasks, want) {
		t.Fatalf("splitTasks() = %#v, want %#v", tasks, want)
	}
}

func TestParseOptions(t *testing.T) {
	arguments := []string{
		"--package", "package.tar.gz",
		"--manifest", "manifest.yaml",
		"--checksum", "package.tar.gz.sha256",
		"--engine", "vllm",
		"--version", "v0.24.0-neutree1",
		"--accelerator", "nvidia_gpu",
		"--image-name", "registry.internal:5000/release/engine-vllm",
		"--image-tag", "v0.24.0-neutree1-ray2.53.0",
		"--supported-tasks", "text-generation, text-embedding",
		"--package-url", "https://files.internal/package.tar.gz",
		"--schema", "schema.json",
		"--template-dir", "templates",
	}
	options, err := parseOptions(arguments)
	if err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	if options.packagePath != "package.tar.gz" || !sameStrings(options.supportedTasks, []string{"text-generation", "text-embedding"}) {
		t.Fatalf("parseOptions() = %#v", options)
	}
	if _, err := parseOptions([]string{"--unknown"}); err == nil {
		t.Fatal("parseOptions() succeeded for an unknown option")
	}
}

func TestValidateOptions(t *testing.T) {
	valid := testVerifyOptions(t)
	if err := validateOptions(valid); err != nil {
		t.Fatalf("validateOptions() error = %v", err)
	}

	missingPackage := valid
	missingPackage.packagePath = ""
	if err := validateOptions(missingPackage); err == nil {
		t.Fatal("validateOptions() succeeded without a package path")
	}

	missingTasks := valid
	missingTasks.supportedTasks = nil
	if err := validateOptions(missingTasks); err == nil {
		t.Fatal("validateOptions() succeeded without supported tasks")
	}

	insecureURL := valid
	insecureURL.packageURL = "http://files.internal/package.tar.gz"
	if err := validateOptions(insecureURL); err == nil {
		t.Fatal("validateOptions() succeeded with an insecure package URL")
	}

	missingSchema := valid
	missingSchema.schemaPath = ""
	if err := validateOptions(missingSchema); err == nil {
		t.Fatal("validateOptions() succeeded without a schema path")
	}

	missingTemplates := valid
	missingTemplates.templateDir = ""
	if err := validateOptions(missingTemplates); err == nil {
		t.Fatal("validateOptions() succeeded without a template directory")
	}
}

func TestVerifyChecksum(t *testing.T) {
	tempDir := t.TempDir()
	packagePath := filepath.Join(tempDir, "package.tar.gz")
	checksumPath := packagePath + ".sha256"
	content := []byte("engine package")
	if err := os.WriteFile(packagePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)

	if err := os.WriteFile(checksumPath, []byte(fmt.Sprintf("%x  %s\n", sum, packagePath)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(packagePath, checksumPath); err != nil {
		t.Fatalf("verifyChecksum() error = %v", err)
	}

	if err := os.WriteFile(checksumPath, []byte(fmt.Sprintf("%x  other.tar.gz\n", sum)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(packagePath, checksumPath); err == nil {
		t.Fatal("verifyChecksum() succeeded with a different filename")
	}

	if err := os.WriteFile(checksumPath, []byte(fmt.Sprintf("%064d  %s\n", 0, packagePath)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(packagePath, checksumPath); err == nil {
		t.Fatal("verifyChecksum() succeeded with a mismatched digest")
	}
}

func TestSHA256File(t *testing.T) {
	tempDir := t.TempDir()
	packagePath := filepath.Join(tempDir, "large-package.tar.gz")
	content := bytes.Repeat([]byte("engine-package-block"), 512*1024)
	if err := os.WriteFile(packagePath, content, 0600); err != nil {
		t.Fatal(err)
	}

	want := sha256.Sum256(content)
	got, err := sha256File(packagePath)
	if err != nil {
		t.Fatalf("sha256File() error = %v", err)
	}
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("sha256File() = %s, want %x", got, want)
	}
}

func TestParseManifest(t *testing.T) {
	if _, err := parseManifest([]byte("engines: [")); err == nil {
		t.Fatal("parseManifest() succeeded for invalid YAML")
	}
	manifest, err := parseManifest([]byte("metadata:\n  package_url: https://files.internal/package.tar.gz\n"))
	if err != nil {
		t.Fatalf("parseManifest() error = %v", err)
	}
	if manifest.Metadata == nil || manifest.Metadata.PackageURL == "" {
		t.Fatal("parseManifest() did not retain metadata.package_url")
	}
}

func TestVerifyManifest(t *testing.T) {
	tempDir := t.TempDir()
	imageFile := "images/vllm-v0.24.0-neutree1-images.tar"
	if err := os.MkdirAll(filepath.Join(tempDir, "images"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, imageFile), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}

	options := testVerifyOptions(t)
	manifest := testManifest(options, imageFile)
	if _, err := verifyManifest(manifest, options, tempDir); err != nil {
		t.Fatalf("verifyManifest() error = %v", err)
	}

	wrongURL := testManifest(options, imageFile)
	wrongURL.Metadata.PackageURL = "https://files.internal/other.tar.gz"
	if _, err := verifyManifest(wrongURL, options, tempDir); err == nil {
		t.Fatal("verifyManifest() succeeded with a mismatched package URL")
	}

	wrongTask := testManifest(options, imageFile)
	wrongTask.Engines[0].EngineVersions[0].SupportedTasks = []string{"text-generation"}
	if _, err := verifyManifest(wrongTask, options, tempDir); err == nil {
		t.Fatal("verifyManifest() succeeded with mismatched tasks")
	}

	wrongImage := testManifest(options, imageFile)
	wrongImage.Engines[0].EngineVersions[0].Images[options.accelerator].Tag = "other"
	if _, err := verifyManifest(wrongImage, options, tempDir); err == nil {
		t.Fatal("verifyManifest() succeeded with a mismatched image")
	}

	extraAccelerator := testManifest(options, imageFile)
	extraAccelerator.Engines[0].EngineVersions[0].Images["amd_gpu"] = &v1.EngineImage{ImageName: "registry.internal/release/engine-vllm-rocm", Tag: "v0.24.0"}
	if _, err := verifyManifest(extraAccelerator, options, tempDir); err == nil {
		t.Fatal("verifyManifest() succeeded with an extra accelerator image")
	}

	extraImage := testManifest(options, imageFile)
	extraImage.Images = append(extraImage.Images, &packageimport.ImageSpec{ImageName: "registry.internal/release/extra", Tag: "v0.24.0", ImageFile: "images/extra-images.tar"})
	if _, err := verifyManifest(extraImage, options, tempDir); err == nil {
		t.Fatal("verifyManifest() succeeded with an extra package image")
	}
}

func TestVerifyImageFiles(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "image.tar"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyImageFiles([]*packageimport.ImageSpec{{ImageFile: "image.tar"}}, tempDir); err != nil {
		t.Fatalf("verifyImageFiles() error = %v", err)
	}
	for _, images := range [][]*packageimport.ImageSpec{
		nil,
		{nil},
		{{ImageFile: "missing.tar"}},
	} {
		if err := verifyImageFiles(images, tempDir); err == nil {
			t.Fatalf("verifyImageFiles(%#v) succeeded", images)
		}
	}
}

func TestVerifySchema(t *testing.T) {
	tempDir := t.TempDir()
	schemaPath := filepath.Join(tempDir, "schema.json")
	if err := os.WriteFile(schemaPath, []byte("{\n  \"type\": \"object\"\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"type":"object"}`))
	if err := verifySchema(map[string]interface{}{"values_schema_base64": encoded}, schemaPath); err != nil {
		t.Fatalf("verifySchema() error = %v", err)
	}

	for _, valuesSchema := range []map[string]interface{}{
		nil,
		{"values_schema_base64": "not-base64"},
		{"values_schema_base64": base64.StdEncoding.EncodeToString([]byte(`{"type":"array"}`))},
	} {
		if err := verifySchema(valuesSchema, schemaPath); err == nil {
			t.Fatalf("verifySchema(%#v) succeeded", valuesSchema)
		}
	}
}

func TestVerifyTemplatesAndLoadTemplates(t *testing.T) {
	tempDir := t.TempDir()
	templateDir := filepath.Join(tempDir, "templates")
	clusterDir := filepath.Join(templateDir, "kubernetes")
	if err := os.MkdirAll(clusterDir, 0755); err != nil {
		t.Fatal(err)
	}
	template := []byte("apiVersion: apps/v1\nkind: Deployment\n")
	if err := os.WriteFile(filepath.Join(clusterDir, "default.yaml"), template, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "ignored.txt"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}

	templates := map[string]map[string]string{
		"kubernetes": {"default": base64.StdEncoding.EncodeToString(template[:len(template)-1])},
	}
	if err := verifyTemplates(templates, templateDir); err != nil {
		t.Fatalf("verifyTemplates() error = %v", err)
	}
	extraTemplate := map[string]map[string]string{
		"kubernetes": {
			"default": base64.StdEncoding.EncodeToString(template[:len(template)-1]),
			"extra":   base64.StdEncoding.EncodeToString(template[:len(template)-1]),
		},
	}
	if err := verifyTemplates(extraTemplate, templateDir); err == nil {
		t.Fatal("verifyTemplates() succeeded with an extra deploy template")
	}
	if err := verifyTemplates(map[string]map[string]string{"kubernetes": {"default": "invalid"}}, templateDir); err == nil {
		t.Fatal("verifyTemplates() succeeded with invalid base64")
	}

	emptyDir := filepath.Join(tempDir, "empty")
	if err := os.MkdirAll(emptyDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTemplates(emptyDir); err == nil {
		t.Fatal("loadTemplates() succeeded without any templates")
	}
	if _, err := loadTemplateModes(filepath.Join(tempDir, "missing")); err == nil {
		t.Fatal("loadTemplateModes() succeeded for a missing directory")
	}
}

func testVerifyOptions(t *testing.T) verifyOptions {
	t.Helper()
	return verifyOptions{
		packagePath:    "package.tar.gz",
		manifestPath:   "manifest.yaml",
		checksumPath:   "package.tar.gz.sha256",
		engineName:     "vllm",
		engineVersion:  "v0.24.0-neutree1",
		accelerator:    "nvidia_gpu",
		imageName:      "registry.internal:5000/release/engine-vllm",
		imageTag:       "v0.24.0-neutree1-ray2.53.0",
		supportedTasks: []string{"text-generation", "text-embedding", "text-rerank"},
		packageURL:     "https://files.internal/vllm-v0.24.0-neutree1.tar.gz",
		schemaPath:     "schema.json",
		templateDir:    "templates",
	}
}

func testManifest(options verifyOptions, imageFile string) *packageimport.PackageManifest {
	return &packageimport.PackageManifest{
		Metadata: &packageimport.PackageMetadata{PackageURL: options.packageURL},
		Images:   []*packageimport.ImageSpec{{ImageName: options.imageName, Tag: options.imageTag, ImageFile: imageFile}},
		Engines: []*packageimport.EngineMetadata{{
			Name: options.engineName,
			EngineVersions: []*v1.EngineVersion{{
				Version:        options.engineVersion,
				SupportedTasks: append([]string(nil), options.supportedTasks...),
				Images: map[string]*v1.EngineImage{
					options.accelerator: {ImageName: options.imageName, Tag: options.imageTag},
				},
			}},
		}},
	}
}

func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
