package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cli/packageimport"
)

type verifyOptions struct {
	packagePath    string
	manifestPath   string
	checksumPath   string
	engineName     string
	engineVersion  string
	accelerator    string
	imageName      string
	imageTag       string
	supportedTasks []string
	packageURL     string
	schemaPath     string
	templateDir    string
}

func main() {
	options, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "verify engine package:", err)
		os.Exit(1)
	}

	if err := verifyPackage(options); err != nil {
		fmt.Fprintln(os.Stderr, "verify engine package:", err)
		os.Exit(1)
	}
}

func parseOptions(arguments []string) (verifyOptions, error) {
	var tasks string
	options := verifyOptions{}
	flags := flag.NewFlagSet("verify-engine-package", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	flags.StringVar(&options.packagePath, "package", "", "path to the engine package archive")
	flags.StringVar(&options.manifestPath, "manifest", "", "path to the standalone manifest")
	flags.StringVar(&options.checksumPath, "checksum", "", "path to the archive checksum")
	flags.StringVar(&options.engineName, "engine", "", "expected engine name")
	flags.StringVar(&options.engineVersion, "version", "", "expected engine version")
	flags.StringVar(&options.accelerator, "accelerator", "", "expected accelerator key")
	flags.StringVar(&options.imageName, "image-name", "", "expected image name without tag")
	flags.StringVar(&options.imageTag, "image-tag", "", "expected image tag")
	flags.StringVar(&tasks, "supported-tasks", "", "expected comma-separated supported tasks")
	flags.StringVar(&options.packageURL, "package-url", "", "expected package URL")
	flags.StringVar(&options.schemaPath, "schema", "", "expected schema JSON path")
	flags.StringVar(&options.templateDir, "template-dir", "", "expected template directory")

	if err := flags.Parse(arguments); err != nil {
		return verifyOptions{}, err
	}

	options.supportedTasks = splitTasks(tasks)

	return options, nil
}

func splitTasks(value string) []string {
	if value == "" {
		return nil
	}

	tasks := strings.Split(value, ",")
	for index := range tasks {
		tasks[index] = strings.TrimSpace(tasks[index])
	}

	return tasks
}

func verifyPackage(options verifyOptions) error {
	if err := validateOptions(options); err != nil {
		return err
	}

	if err := verifyChecksum(options.packagePath, options.checksumPath); err != nil {
		return err
	}

	extractDir, err := os.MkdirTemp("", "verify-engine-package-")
	if err != nil {
		return fmt.Errorf("create extraction directory: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := packageimport.NewExtractor().Extract(options.packagePath, extractDir); err != nil {
		return fmt.Errorf("extract package: %w", err)
	}

	archiveManifestPath := filepath.Join(extractDir, packageimport.ManifestFileName)
	archiveManifest, err := os.ReadFile(archiveManifestPath)

	if err != nil {
		return fmt.Errorf("read archive manifest: %w", err)
	}

	standaloneManifest, err := os.ReadFile(options.manifestPath)
	if err != nil {
		return fmt.Errorf("read standalone manifest: %w", err)
	}

	if !bytes.Equal(archiveManifest, standaloneManifest) {
		return fmt.Errorf("standalone manifest does not match archive manifest")
	}

	manifest, err := parseManifest(archiveManifest)
	if err != nil {
		return err
	}

	engineVersion, err := verifyManifest(manifest, options, extractDir)
	if err != nil {
		return err
	}

	if err := verifySchema(engineVersion.ValuesSchema, options.schemaPath); err != nil {
		return err
	}

	return verifyTemplates(engineVersion.DeployTemplate, options.templateDir)
}

func validateOptions(options verifyOptions) error {
	required := map[string]string{
		"package":      options.packagePath,
		"manifest":     options.manifestPath,
		"checksum":     options.checksumPath,
		"engine":       options.engineName,
		"version":      options.engineVersion,
		"accelerator":  options.accelerator,
		"image-name":   options.imageName,
		"image-tag":    options.imageTag,
		"package-url":  options.packageURL,
		"schema":       options.schemaPath,
		"template-dir": options.templateDir,
	}
	for name, value := range required {
		if value == "" {
			return fmt.Errorf("--%s is required", name)
		}
	}

	if len(options.supportedTasks) == 0 {
		return fmt.Errorf("--supported-tasks is required")
	}

	if err := validateHTTPSURL(options.packageURL); err != nil {
		return err
	}

	return nil
}

func validateHTTPSURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("--package-url must be an HTTPS URL without credentials, query, or fragment")
	}

	return nil
}

func verifyChecksum(packagePath, checksumPath string) error {
	checksumContent, err := os.ReadFile(checksumPath)
	if err != nil {
		return fmt.Errorf("read checksum: %w", err)
	}

	fields := strings.Fields(string(checksumContent))
	if len(fields) != 2 || filepath.Base(strings.TrimPrefix(fields[1], "*")) != filepath.Base(packagePath) {
		return fmt.Errorf("invalid checksum file %s", checksumPath)
	}

	actualChecksum, err := sha256File(packagePath)
	if err != nil {
		return fmt.Errorf("hash package: %w", err)
	}

	if fields[0] != actualChecksum {
		return fmt.Errorf("checksum does not match package")
	}

	return nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func parseManifest(content []byte) (*packageimport.PackageManifest, error) {
	manifest := &packageimport.PackageManifest{}
	if err := yaml.Unmarshal(content, manifest); err != nil {
		return nil, fmt.Errorf("parse package manifest: %w", err)
	}

	return manifest, nil
}

func verifyManifest(manifest *packageimport.PackageManifest, options verifyOptions, extractDir string) (*v1.EngineVersion, error) {
	if manifest.Metadata == nil || manifest.Metadata.PackageURL != options.packageURL {
		return nil, fmt.Errorf("manifest package_url does not match expected URL")
	}

	if len(manifest.Engines) != 1 || manifest.Engines[0].Name != options.engineName {
		return nil, fmt.Errorf("manifest engine does not match %s", options.engineName)
	}

	engine := manifest.Engines[0]
	if len(engine.EngineVersions) != 1 || engine.EngineVersions[0].Version != options.engineVersion {
		return nil, fmt.Errorf("manifest engine version does not match %s", options.engineVersion)
	}

	if !reflect.DeepEqual(engine.EngineVersions[0].SupportedTasks, options.supportedTasks) {
		return nil, fmt.Errorf("manifest supported_tasks do not match expected tasks")
	}

	engineVersion := engine.EngineVersions[0]
	if len(engineVersion.Images) != 1 {
		return nil, fmt.Errorf("manifest engine images must contain only %s", options.accelerator)
	}

	image, ok := engineVersion.Images[options.accelerator]
	if !ok || image == nil || image.ImageName != options.imageName || image.Tag != options.imageTag {
		return nil, fmt.Errorf("manifest image for %s does not match expected image", options.accelerator)
	}

	expectedImageFile := filepath.ToSlash(filepath.Join("images", fmt.Sprintf("%s-%s-images.tar", options.engineName, options.engineVersion)))
	if err := verifyReleaseImage(manifest.Images, options, expectedImageFile, extractDir); err != nil {
		return nil, err
	}

	return engineVersion, nil
}

func verifyReleaseImage(images []*packageimport.ImageSpec, options verifyOptions, expectedImageFile, extractDir string) error {
	if len(images) != 1 {
		return fmt.Errorf("manifest package images must contain exactly one expected image")
	}

	image := images[0]
	if image == nil || image.ImageName != options.imageName || image.Tag != options.imageTag || image.ImageFile != expectedImageFile {
		return fmt.Errorf("manifest package image does not match expected image tuple")
	}

	return verifyImageFiles(images, extractDir)
}

func verifyImageFiles(images []*packageimport.ImageSpec, extractDir string) error {
	if len(images) == 0 {
		return fmt.Errorf("manifest has no package images")
	}

	for _, image := range images {
		if image == nil || image.ImageFile == "" {
			return fmt.Errorf("manifest image file is missing")
		}

		if _, err := os.Stat(filepath.Join(extractDir, image.ImageFile)); err != nil {
			return fmt.Errorf("package image file %s is missing: %w", image.ImageFile, err)
		}
	}

	return nil
}

func verifySchema(valuesSchema map[string]interface{}, schemaPath string) error {
	encoded, ok := valuesSchema["values_schema_base64"].(string)
	if !ok {
		return fmt.Errorf("manifest values schema is not base64 encoded")
	}

	actualSchema, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decode manifest values schema: %w", err)
	}

	expectedSchema, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read expected schema: %w", err)
	}

	var (
		actualValue   interface{}
		expectedValue interface{}
	)

	if err := json.Unmarshal(actualSchema, &actualValue); err != nil {
		return fmt.Errorf("parse manifest values schema: %w", err)
	}

	if err := json.Unmarshal(expectedSchema, &expectedValue); err != nil {
		return fmt.Errorf("parse expected schema: %w", err)
	}

	if !reflect.DeepEqual(actualValue, expectedValue) {
		return fmt.Errorf("manifest values schema does not match %s", schemaPath)
	}

	return nil
}

func verifyTemplates(templates map[string]map[string]string, templateDir string) error {
	expectedTemplates, err := loadTemplates(templateDir)
	if err != nil {
		return err
	}

	if len(templates) != len(expectedTemplates) {
		return fmt.Errorf("manifest deploy templates do not match expected template set")
	}

	for clusterType, modes := range expectedTemplates {
		actualModes, ok := templates[clusterType]
		if !ok || len(actualModes) != len(modes) {
			return fmt.Errorf("manifest deploy templates do not match expected template set")
		}

		for mode, expected := range modes {
			encoded, ok := actualModes[mode]
			if !ok {
				return fmt.Errorf("manifest deploy templates do not match expected template set")
			}

			actual, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return fmt.Errorf("decode %s/%s template: %w", clusterType, mode, err)
			}
			// The package builder reads templates through command substitution, which
			// removes trailing line feeds before base64 encoding them.
			if !bytes.Equal(actual, bytes.TrimRight(expected, "\n")) {
				return fmt.Errorf("manifest template %s/%s does not match %s", clusterType, mode, templateDir)
			}
		}
	}

	return nil
}

func loadTemplates(templateDir string) (map[string]map[string][]byte, error) {
	templates := make(map[string]map[string][]byte)
	entries, err := os.ReadDir(templateDir)

	if err != nil {
		return nil, fmt.Errorf("read template directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		modes, err := loadTemplateModes(filepath.Join(templateDir, entry.Name()))
		if err != nil {
			return nil, err
		}

		if len(modes) > 0 {
			templates[entry.Name()] = modes
		}
	}

	if len(templates) == 0 {
		return nil, fmt.Errorf("no templates found in %s", templateDir)
	}

	return templates, nil
}

func loadTemplateModes(clusterTypeDir string) (map[string][]byte, error) {
	modes := make(map[string][]byte)
	entries, err := os.ReadDir(clusterTypeDir)

	if err != nil {
		return nil, fmt.Errorf("read template directory %s: %w", clusterTypeDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml") {
			continue
		}

		content, err := os.ReadFile(filepath.Join(clusterTypeDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read template %s: %w", entry.Name(), err)
		}

		modes[strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".yaml"), ".yml")] = content
	}

	return modes, nil
}
