package packageimport

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
		manifests, err := p.parseYAMLManifest(manifestPath)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse YAML manifest")
		}

		err = p.validateManifest(manifests, extractedPath)
		if err != nil {
			return nil, errors.Wrap(err, "manifest validation failed")
		}

		return manifests, nil
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

	// Process each engine version
	for idx := range manifest.Engines {
		for vidx := range manifest.Engines[idx].EngineVersions {
			ev := manifest.Engines[idx].EngineVersions[vidx]
			// Decode base64-encoded values schema if present
			valueSchemaStr, ok := ev.ValuesSchema["values_schema_base64"]
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

				ev.ValuesSchema = decodedSchema
			}

			// set default tag and extract image name without registry
			for i := range ev.Images {
				if ev.Images[i].Tag == "" {
					ev.Images[i].Tag = ev.Version
				}

				ev.Images[i].ImageName = extractImageNameWithoutRegistry(ev.Images[i].ImageName)
			}

			manifest.Engines[idx].EngineVersions[vidx] = ev
		}
	}

	return &manifest, nil
}

func (p *Parser) validateManifest(manifest *PackageManifest, extractedPath string) error {
	if manifest == nil {
		return errors.New("manifest is nil")
	}

	if len(manifest.Images) == 0 {
		return errors.New("no images specified")
	}

	// Validate each image spec
	for i, img := range manifest.Images {
		if img.ImageName == "" {
			return errors.Errorf("image %d: image name is empty", i)
		}

		if img.Tag == "" {
			return errors.Errorf("image %d: tag is empty", i)
		}

		imagePath := filepath.Join(extractedPath, img.ImageFile)
		if _, err := os.Stat(imagePath); os.IsNotExist(err) {
			return errors.Errorf("image file not found: %s", img.ImageFile)
		}
	}

	for idx := range manifest.Engines {
		if err := p.validateEngineConfig(manifest.Engines[idx]); err != nil {
			return errors.Wrap(err, "invalid engine configuration in manifest")
		}
	}

	return nil
}

func (p *Parser) validateEngineConfig(engine *EngineMetadata) error {
	if engine == nil {
		return errors.New("engine is nil")
	}

	if engine.Name == "" {
		return errors.New("engine name is empty")
	}

	if len(engine.EngineVersions) == 0 {
		return errors.New("no engine versions defined")
	}

	for _, ev := range engine.EngineVersions {
		if ev == nil {
			return errors.New("engine version definition is nil")
		}

		if ev.Version == "" {
			return errors.New("engine version field is empty")
		}
	}

	return nil
}
