package packageimport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"

	"github.com/docker/docker/api/types/registry"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/client"
)

// Importer handles importing packages
type Importer struct {
	apiClient *client.Client
	extractor *Extractor
	parser    *Parser
	validator *Validator
}

// NewImporter creates a new Importer
func NewImporter(apiClient *client.Client) *Importer {
	return &Importer{
		apiClient: apiClient,
		extractor: NewExtractor(),
		parser:    NewParser(),
		validator: NewValidator(),
	}
}

// Import imports a package
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
		tempDir, err := os.MkdirTemp("", "neutree-*")
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

	// Push images
	klog.Info("Pushing images to registry")

	pushedImages, err := i.pushImages(ctx, opts, manifest)
	if err != nil {
		return nil, errors.Wrap(err, "failed to push images to registry")
	}

	result.ImagesImported = pushedImages

	if len(manifest.Engines) > 0 {
		klog.Infof("Engines to import: %d", len(manifest.Engines))

		for _, engine := range manifest.Engines {
			if err := i.updateEngine(ctx, engine, opts); err != nil {
				result.Errors = append(result.Errors, err)
				return result, errors.Wrap(err, "failed to process engine upload")
			}

			klog.Infof("Successfully imported engine %s", engine.Name)
		}

		result.EnginesImported = manifest.Engines
	}

	return result, nil
}

func (i *Importer) pushImages(ctx context.Context, opts *ImportOptions, manifest *PackageManifest) ([]string, error) {
	klog.Info("Loading and pushing images to registry")

	if opts.SkipImageLoad {
		klog.Info("Skipping image load as per configuration")
		return []string{}, nil
	}

	imagePusher, err := NewImagePusher()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create image pusher")
	}

	err = imagePusher.LoadImages(ctx, manifest, opts.ExtractPath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load images")
	}

	if opts.SkipImagePush {
		klog.Info("Skipping image push as per configuration")
		return []string{}, nil
	}

	mirrorRegistry := opts.MirrorRegistry
	user, token := opts.RegistryUser, opts.RegistryPassword

	if opts.ImageRegistry != "" {
		imgRegistrys, err := i.apiClient.ImageRegistries.List(client.ImageRegistryListOptions{
			Workspace: opts.Workspace,
			Name:      opts.ImageRegistry,
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to get image registry")
		}

		if len(imgRegistrys) == 0 {
			return nil, errors.Errorf("image registry %s not found", opts.ImageRegistry)
		}

		targetRegistry := &imgRegistrys[0]

		klog.Info("Pushing image to registry")

		mirrorRegistry, err = util.GetImagePrefix(targetRegistry)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get image prefix")
		}

		user, token = util.GetImageRegistryAuthInfo(targetRegistry)
	}

	authConfig := registry.AuthConfig{
		Username:      user,
		Password:      token,
		ServerAddress: mirrorRegistry,
	}

	authConfigBytes, err := json.Marshal(authConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal auth config")
	}

	registryAuth := base64.URLEncoding.EncodeToString(authConfigBytes)

	pushedImages, err := imagePusher.PushImagesToMirrorRegistry(ctx, mirrorRegistry, registryAuth, manifest)
	if err != nil {
		return nil, errors.Wrap(err, "failed to push images to mirror registry")
	}

	return pushedImages, nil
}

// validateOptions validates the import options
func (i *Importer) validateOptions(opts *ImportOptions) error {
	if opts.PackagePath == "" {
		return errors.New("package path is required")
	}

	if _, err := os.Stat(opts.PackagePath); os.IsNotExist(err) {
		return errors.Errorf("package file not found: %s", opts.PackagePath)
	}

	if opts.SkipImageLoad && !opts.SkipImagePush {
		return errors.New("cannot skip image load when image push is enabled")
	}

	if !opts.SkipImagePush {
		if (opts.Workspace == "" || opts.ImageRegistry == "") && (opts.MirrorRegistry == "" || opts.RegistryUser == "" || opts.RegistryPassword == "") {
			return errors.New("image registry config is required when not skipping image push")
		}
	}

	return nil
}

// updateEngine updates the engine with the new version
func (i *Importer) updateEngine(_ context.Context, engineMetadata *EngineMetadata, opts *ImportOptions) error {
	newEngine := &v1.Engine{
		APIVersion: "v1",
		Kind:       "Engine",
		Metadata: &v1.Metadata{
			Name:      engineMetadata.Name,
			Workspace: opts.Workspace,
		},
		Spec: &v1.EngineSpec{
			Versions:       engineMetadata.EngineVersions,
			SupportedTasks: engineMetadata.SupportedTasks,
		},
	}

	engineList, err := i.apiClient.Engines.List(client.ListOptions{
		Workspace: opts.Workspace,
		Name:      engineMetadata.Name,
	})

	if err != nil {
		return errors.Wrap(err, "failed to check if engine exists")
	}

	if len(engineList) == 0 {
		// Create new engine
		return i.apiClient.Engines.Create(opts.Workspace, newEngine)
	}

	existedEngine := &engineList[0]

	// Update existing engine
	// Check if version already exists and remove it if force is enabled
	for _, newVersion := range newEngine.Spec.Versions {
		found := false

		for idx, oldVersion := range existedEngine.Spec.Versions {
			if oldVersion.Version == newVersion.Version && opts.Force {
				// merge
				existedEngine.Spec.Versions[idx] = util.MergeEngineVersion(oldVersion, newVersion)
				found = true

				break
			}
		}

		if !found {
			existedEngine.Spec.Versions = append(existedEngine.Spec.Versions, newVersion)
		}
	}

	return i.apiClient.Engines.Update(opts.Workspace, existedEngine.GetID(), existedEngine)
}

// Validator handles validation of packages
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

// ValidatePackage validates a package without importing it
func (v *Validator) ValidatePackage(packagePath string) error {
	// Create temporary directory
	tempDir, err := os.MkdirTemp("", "neutree-validate-*")
	if err != nil {
		return errors.Wrap(err, "failed to create temporary directory")
	}
	defer os.RemoveAll(tempDir)

	// Extract the package
	if err := v.extractor.Extract(packagePath, tempDir); err != nil {
		return errors.Wrap(err, "failed to extract package")
	}

	// Parse the manifest
	_, err = v.parser.ParseManifest(tempDir)
	if err != nil {
		return errors.Wrap(err, "failed to parse manifest")
	}

	klog.Info("Package validation successful")

	return nil
}

// ValidatePackage is a convenience function that creates a validator and validates a package
func ValidatePackage(packagePath string) error {
	v := NewValidator()
	return v.ValidatePackage(packagePath)
}
