package engine_version

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

const (
	// ManifestFileName is the name of the manifest file in the package
	ManifestFileName = "manifest.yaml"
)

// Parser handles parsing of engine version package manifests
type Parser struct{}

// NewParser creates a new Parser
func NewParser() *Parser {
	return &Parser{}
}

// ParseManifest parses the manifest file from the extracted package directory
func (p *Parser) ParseManifest(extractedPath string) (*PackageManifest, error) {
	manifestPath := filepath.Join(extractedPath, ManifestFileName)
	if _, err := os.Stat(manifestPath); err == nil {
		return p.parseYAMLManifest(manifestPath)
	}

	return nil, errors.New("manifest file not found")
}

// parseYAMLManifest parses a YAML manifest file
func (p *Parser) parseYAMLManifest(path string) (*PackageManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read manifest file")
	}

	var manifest PackageManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal YAML manifest")
	}

	// Decode base64-encoded values schema if present
	valueSchemaStr, ok := manifest.Package.EngineVersion.ValuesSchema["values_schema_base64"]
	if ok {
		valueSchemaBase64Str, ok := valueSchemaStr.(string)
		if !ok {
			return nil, errors.New("invalid values_schema_base64 format in manifest")
		}

		valueSchemaJson, err := base64.StdEncoding.DecodeString(valueSchemaBase64Str)
		if err != nil {
			return nil, errors.Wrap(err, "failed to decode values schema from base64")
		}

		var decodedSchema map[string]interface{}
		if err := json.Unmarshal(valueSchemaJson, &decodedSchema); err != nil {
			return nil, errors.Wrap(err, "failed to unmarshal values schema JSON")
		}

		manifest.Package.EngineVersion.ValuesSchema = decodedSchema
	}

	if err := p.validateManifest(&manifest); err != nil {
		return nil, errors.Wrap(err, "manifest validation failed")
	}

	return &manifest, nil
}

// validateManifest validates the parsed manifest
func (p *Parser) validateManifest(manifest *PackageManifest) error {
	if manifest.Package == nil {
		return errors.New("package is nil")
	}

	if manifest.Package.Metadata == nil {
		return errors.New("package metadata is nil")
	}

	if manifest.Package.Metadata.EngineName == "" {
		return errors.New("engine name is empty")
	}

	if manifest.Package.Metadata.Version == "" {
		return errors.New("version is empty")
	}

	if manifest.Package.EngineVersion == nil {
		return errors.New("engine version is nil")
	}

	if len(manifest.Package.Images) == 0 {
		return errors.New("no images specified")
	}

	// Validate each image spec
	for i, img := range manifest.Package.Images {
		if img.Accelerator == "" {
			return errors.Errorf("image %d: accelerator is empty", i)
		}

		if img.ImageName == "" {
			return errors.Errorf("image %d: image name is empty", i)
		}

		if img.Tag == "" {
			return errors.Errorf("image %d: tag is empty", i)
		}

		if img.ImageFile == "" {
			return errors.Errorf("image %d: image file is empty", i)
		}
	}

	return nil
}

// GetImagePath returns the full path to an image file in the extracted package
func (p *Parser) GetImagePath(extractedPath string, imageFile string) string {
	return filepath.Join(extractedPath, imageFile)
}
