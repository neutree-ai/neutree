package engine_version

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/client"
)

// Importer handles importing engine version packages
type Importer struct {
	apiClient   *client.Client
	extractor   *Extractor
	parser      *Parser
	imagePusher *ImagePusher
}

// NewImporter creates a new Importer
func NewImporter(apiClient *client.Client) *Importer {
	return &Importer{
		apiClient:   apiClient,
		extractor:   NewExtractor(),
		parser:      NewParser(),
		imagePusher: NewImagePusher(),
	}
}

// Import imports an engine version package
func (i *Importer) Import(ctx context.Context, opts *ImportOptions) (*ImportResult, error) {
	result := &ImportResult{
		ImagesImported: []string{},
		Errors:         []error{},
	}

	// Validate options
	if err := i.validateOptions(opts); err != nil {
		return nil, errors.Wrap(err, "invalid import options")
	}

	// Create temporary directory if not specified
	if opts.ExtractPath == "" {
		tempDir, err := os.MkdirTemp("", "engine-version-*")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temporary directory")
		}

		opts.ExtractPath = tempDir

		defer os.RemoveAll(tempDir)
	}

	klog.Infof("Extracting package to %s", opts.ExtractPath)

	// Extract the package
	if err := i.extractor.Extract(opts.PackagePath, opts.ExtractPath); err != nil {
		return nil, errors.Wrap(err, "failed to extract package")
	}

	// Parse the manifest
	klog.Info("Parsing manifest")

	manifest, err := i.parser.ParseManifest(opts.ExtractPath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse manifest")
	}

	result.EngineName = manifest.Package.Metadata.EngineName
	result.Version = manifest.Package.Metadata.Version

	// Check if engine exists
	engineList, err := i.apiClient.Engines.List(client.ListOptions{
		Workspace: opts.Workspace,
		Name:      manifest.Package.Metadata.EngineName,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to check if engine exists")
	}

	var engine *v1.Engine
	if len(engineList) > 0 {
		engine = &engineList[0]
		klog.Infof("Found existing engine: %s", engine.Metadata.Name)

		// Check if version already exists
		if !opts.Force {
			for _, ver := range engine.Spec.Versions {
				if ver.Version == manifest.Package.Metadata.Version {
					return nil, errors.Errorf("engine version %s already exists for engine %s (use --force to overwrite)",
						manifest.Package.Metadata.Version, manifest.Package.Metadata.EngineName)
				}
			}
		}
	}

	// Push images to registry if not skipped
	if !opts.SkipImagePush {
		klog.Info("Loading and pushing images to registry")

		imageRegistry, err := i.apiClient.ImageRegistries.Get(opts.Workspace, opts.ImageRegistry)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get image registry")
		}

		klog.Infof("Using image registry: %s", imageRegistry.Metadata.Name)

		registryHost, err := util.GetImageRegistryHost(imageRegistry)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse image registry URL")
		}

		// Login to the image registry
		userName, password := util.GetImageRegistryAuthInfo(imageRegistry)
		if userName != "" && password != "" {
			cmd := exec.CommandContext(ctx, "docker", "login", registryHost, "-u", userName, "-p", password)

			output, err := cmd.CombinedOutput()
			if err != nil {
				return nil, errors.Wrapf(err, "docker login failed: %s", string(output))
			}
		}

		pushedImages, err := i.imagePusher.LoadAndPushImages(
			ctx,
			manifest,
			opts.ExtractPath,
			registryHost,
			imageRegistry.Spec.Repository,
		)
		if err != nil {
			result.Errors = append(result.Errors, err)
			return result, errors.Wrap(err, "failed to push images")
		}

		result.ImagesImported = pushedImages
	}

	// Update or create engine
	klog.Info("Updating engine definition")

	if err := i.updateEngine(ctx, engine, manifest, opts); err != nil {
		result.Errors = append(result.Errors, err)
		return result, errors.Wrap(err, "failed to update engine")
	}

	result.EngineUpdated = true
	klog.Infof("Successfully imported engine version %s:%s", result.EngineName, result.Version)

	return result, nil
}

// validateOptions validates the import options
func (i *Importer) validateOptions(opts *ImportOptions) error {
	if opts.PackagePath == "" {
		return errors.New("package path is required")
	}

	if _, err := os.Stat(opts.PackagePath); os.IsNotExist(err) {
		return errors.Errorf("package file not found: %s", opts.PackagePath)
	}

	if !opts.SkipImagePush {
		if opts.ImageRegistry == "" {
			return errors.New("image registry is required when not skipping image push")
		}
	}

	return nil
}

// updateEngine updates the engine with the new version
func (i *Importer) updateEngine(_ context.Context, engine *v1.Engine, manifest *PackageManifest, opts *ImportOptions) error {
	newVersion := manifest.Package.EngineVersion

	// Ensure the version field matches the metadata
	newVersion.Version = manifest.Package.Metadata.Version

	if engine == nil {
		// Create new engine
		engine = &v1.Engine{
			APIVersion: "v1",
			Kind:       "Engine",
			Metadata: &v1.Metadata{
				Name:      manifest.Package.Metadata.EngineName,
				Workspace: opts.Workspace,
			},
			Spec: &v1.EngineSpec{
				Versions:       []*v1.EngineVersion{newVersion},
				SupportedTasks: []string{}, // Will be populated from manifest if available
			},
		}

		return i.apiClient.Engines.Create(opts.Workspace, engine)
	}

	// Update existing engine
	// Check if version already exists and remove it if force is enabled

	var oldVersion *v1.EngineVersion

	for idx, ver := range engine.Spec.Versions {
		if ver.Version == manifest.Package.Metadata.Version {
			// Remove the old version
			engine.Spec.Versions = append(engine.Spec.Versions[:idx], engine.Spec.Versions[idx+1:]...)
			oldVersion = ver

			break
		}
	}

	if oldVersion == nil || opts.Force {
		engine.Spec.Versions = append(engine.Spec.Versions, newVersion)
	} else {
		// merge oldVersion with newVersion
		for key := range newVersion.Images {
			oldVersion.Images[key] = newVersion.Images[key]
		}

		for clusterType := range newVersion.DeployTemplate {
			for deployMode := range newVersion.DeployTemplate[clusterType] {
				oldVersion.DeployTemplate[clusterType][deployMode] = newVersion.DeployTemplate[clusterType][deployMode]
			}
		}

		if newVersion.ValuesSchema != nil {
			oldVersion.ValuesSchema = newVersion.ValuesSchema
		}

		for idx := range newVersion.SupportedTasks {
			found := false

			for _, oldTask := range oldVersion.SupportedTasks {
				if oldTask == newVersion.SupportedTasks[idx] {
					found = true
					break
				}
			}

			if !found {
				oldVersion.SupportedTasks = append(oldVersion.SupportedTasks, newVersion.SupportedTasks[idx])
			}
		}
	}

	return i.apiClient.Engines.Update(opts.Workspace, engine.GetID(), engine)
}

// Validator handles validation of engine version packages
type Validator struct {
	extractor *Extractor
	parser    *Parser
}

// NewValidator creates a new Validator
func NewValidator() *Validator {
	return &Validator{
		extractor: NewExtractor(),
		parser:    NewParser(),
	}
}

// ValidatePackage validates an engine version package without importing it
func (v *Validator) ValidatePackage(packagePath string) error {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "engine-version-validate-*")
	if err != nil {
		return errors.Wrap(err, "failed to create temporary directory")
	}
	defer os.RemoveAll(tempDir)

	// Extract the package
	if err := v.extractor.Extract(packagePath, tempDir); err != nil {
		return errors.Wrap(err, "failed to extract package")
	}

	// Parse the manifest
	manifest, err := v.parser.ParseManifest(tempDir)
	if err != nil {
		return errors.Wrap(err, "failed to parse manifest")
	}

	// Validate that all image files exist
	for _, imgSpec := range manifest.Package.Images {
		imagePath := filepath.Join(tempDir, imgSpec.ImageFile)
		if _, err := os.Stat(imagePath); os.IsNotExist(err) {
			return errors.Errorf("image file not found: %s", imgSpec.ImageFile)
		}
	}

	klog.Info("Package validation successful")

	return nil
}

// ValidatePackage is a convenience function that creates a validator and validates a package
func ValidatePackage(packagePath string) error {
	v := NewValidator()
	return v.ValidatePackage(packagePath)
}
