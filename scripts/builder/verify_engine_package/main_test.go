package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyPackage(t *testing.T) {
	tempDir := t.TempDir()
	schemaPath := filepath.Join(tempDir, "schema.json")
	templateDir := filepath.Join(tempDir, "templates")
	packagePath := filepath.Join(tempDir, "vllm-v0.24.0-neutree1.tar.gz")
	manifestPath := filepath.Join(tempDir, "vllm-v0.24.0-neutree1-manifest.yaml")
	checksumPath := packagePath + ".sha256"
	packageURL := "https://files.internal/engine-packages/vllm/v0.24.0-neutree1/nvidia/v0.24.0-neutree1-ray2.53.0/vllm-v0.24.0-neutree1.tar.gz"
	imageName := "registry.internal:5000/release/engine-vllm"
	imageTag := "v0.24.0-neutree1-ray2.53.0"
	schema := []byte(`{"type":"object","properties":{"max_model_len":{"type":"integer"}}}`)
	template := []byte("apiVersion: apps/v1\nkind: Deployment\n")

	if err := os.WriteFile(schemaPath, schema, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(templateDir, "kubernetes"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(templateDir, "kubernetes", "default.yaml"), template, 0600); err != nil {
		t.Fatal(err)
	}

	manifest := []byte(fmt.Sprintf(`manifest_version: "1.0"
metadata:
  package_url: %q
images:
  - accelerator: "nvidia_gpu"
    image_name: %q
    tag: %q
    image_file: "images/vllm-v0.24.0-neutree1-images.tar"
engines:
  - name: "vllm"
    engine_versions:
      - version: "v0.24.0-neutree1"
        values_schema:
          values_schema_base64: %q
        deploy_template:
          kubernetes:
            default: %q
        supported_tasks:
          - "text-generation"
          - "text-embedding"
          - "text-rerank"
        images:
          nvidia_gpu:
            image_name: %q
            tag: %q
`, packageURL, imageName, imageTag, base64.StdEncoding.EncodeToString(schema), base64.StdEncoding.EncodeToString(bytes.TrimRight(template, "\n")), imageName, imageTag))

	writeTestPackage(t, packagePath, map[string][]byte{
		"manifest.yaml": manifest,
		"images/vllm-v0.24.0-neutree1-images.tar": []byte("image archive"),
	})
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}

	archive, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	checksum := sha256.Sum256(archive)
	if err := os.WriteFile(checksumPath, []byte(fmt.Sprintf("%x  %s\n", checksum, packagePath)), 0600); err != nil {
		t.Fatal(err)
	}

	options := verifyOptions{
		packagePath:    packagePath,
		manifestPath:   manifestPath,
		checksumPath:   checksumPath,
		engineName:     "vllm",
		engineVersion:  "v0.24.0-neutree1",
		accelerator:    "nvidia_gpu",
		imageName:      imageName,
		imageTag:       imageTag,
		supportedTasks: []string{"text-generation", "text-embedding", "text-rerank"},
		packageURL:     packageURL,
		schemaPath:     schemaPath,
		templateDir:    templateDir,
	}

	if err := verifyPackage(options); err != nil {
		t.Fatalf("verifyPackage() error = %v", err)
	}

	options.packageURL = "https://files.internal/other.tar.gz"
	if err := verifyPackage(options); err == nil {
		t.Fatal("verifyPackage() succeeded with a mismatched package URL")
	}
}

func TestVerifyPackageChecksChecksumBeforeExtracting(t *testing.T) {
	tempDir := t.TempDir()
	packagePath := filepath.Join(tempDir, "package.tar.gz")
	checksumPath := packagePath + ".sha256"
	manifestPath := filepath.Join(tempDir, "manifest.yaml")
	if err := os.WriteFile(packagePath, []byte("not a tar archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checksumPath, []byte("0000000000000000000000000000000000000000000000000000000000000000  package.tar.gz\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte("manifest_version: '1.0'\n"), 0600); err != nil {
		t.Fatal(err)
	}

	options := verifyOptions{
		packagePath:    packagePath,
		manifestPath:   manifestPath,
		checksumPath:   checksumPath,
		engineName:     "vllm",
		engineVersion:  "v0.24.0",
		accelerator:    "nvidia_gpu",
		imageName:      "registry.internal/release/engine-vllm",
		imageTag:       "v0.24.0",
		supportedTasks: []string{"text-generation"},
		packageURL:     "https://files.internal/engine-packages/vllm/v0.24.0/nvidia/v0.24.0/vllm-v0.24.0.tar.gz",
		schemaPath:     "schema.json",
		templateDir:    "templates",
	}
	if err := verifyPackage(options); err == nil || !strings.Contains(err.Error(), "checksum does not match package") {
		t.Fatalf("verifyPackage() error = %v, want checksum validation error", err)
	}
}

func writeTestPackage(t *testing.T, packagePath string, files map[string][]byte) {
	t.Helper()

	file, err := os.Create(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	for name, content := range files {
		header := &tar.Header{Name: name, Mode: 0600, Size: int64(len(content))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
}
