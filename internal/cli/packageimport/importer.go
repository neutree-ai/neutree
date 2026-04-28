package packageimport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// isManifestFile returns true if the path points to a standalone manifest YAML file.
func isManifestFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
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

	if isManifestFile(opts.PackagePath) {
		return i.importFromManifest(ctx, opts, result)
	}

	return i.importFromArchive(ctx, opts, result)
}

// importFromManifest handles manifest-only mode import.
func (i *Importer) importFromManifest(ctx context.Context, opts *ImportOptions, result *ImportResult) (*ImportResult, error) {
	klog.Info("Parsing standalone manifest file")

	manifest, err := i.parser.ParseManifestFile(opts.PackagePath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse manifest file")
	}

	// If skip image load, just register engine metadata
	if opts.SkipImageLoad {
		klog.Info("Skipping image handling as per configuration")
		return i.registerEngines(ctx, opts, manifest, result)
	}

	// If package_url is present, stream-extract and push images
	if manifest.Metadata != nil && manifest.Metadata.PackageURL != "" {
		klog.Infof("Package URL found: %s", manifest.Metadata.PackageURL)

		// Ensure the parent directory exists when user specifies a custom extract path.
		if opts.ExtractPath != "" {
			if err := os.MkdirAll(opts.ExtractPath, 0o755); err != nil {
				return nil, errors.Wrap(err, "failed to create extract path directory")
			}
		}

		// Create a unique temporary directory to avoid concurrent import overwrites.
		tempDir, err := os.MkdirTemp(opts.ExtractPath, "neutree-*")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create temporary directory")
		}

		opts.ExtractPath = tempDir

		defer os.RemoveAll(tempDir)

		// Stream-extract the package directly from HTTP response
		klog.Infof("Downloading and extracting package to %s", opts.ExtractPath)

		if err := streamExtractPackage(ctx, manifest.Metadata.PackageURL, i.extractor, opts.ExtractPath); err != nil {
			return nil, errors.Wrap(err, "failed to download and extract package from URL")
		}

		// Re-parse manifest from extracted content to get validated image references
		manifest, err = i.parser.ParseManifest(opts.ExtractPath)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse manifest from extracted package")
		}

		// Push images
		klog.Info("Pushing images to registry")

		pushedImages, err := i.pushImages(ctx, opts, manifest)
		if err != nil {
			return nil, errors.Wrap(err, "failed to push images to registry")
		}

		result.ImagesImported = pushedImages

		return i.registerEngines(ctx, opts, manifest, result)
	}

	// No package_url and not skipping images — register metadata only
	klog.Info("No package URL specified, registering engine metadata only (no images to process)")

	return i.registerEngines(ctx, opts, manifest, result)
}

// importFromArchive handles the traditional tar.gz archive import flow.
func (i *Importer) importFromArchive(ctx context.Context, opts *ImportOptions, result *ImportResult) (*ImportResult, error) {
	// Ensure the parent directory exists when user specifies a custom extract path.
	if opts.ExtractPath != "" {
		if err := os.MkdirAll(opts.ExtractPath, 0o755); err != nil {
			return nil, errors.Wrap(err, "failed to create extract path directory")
		}
	}

	// Create a unique temporary directory to avoid concurrent import overwrites.
	// When ExtractPath is empty, os.MkdirTemp uses the system default temp dir.
	// When ExtractPath is set, a unique subdirectory is created under it.
	tempDir, err := os.MkdirTemp(opts.ExtractPath, "neutree-*")
	if err != nil {
		return nil, errors.Wrap(err, "failed to create temporary directory")
	}

	opts.ExtractPath = tempDir

	defer os.RemoveAll(tempDir)

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

	return i.registerEngines(ctx, opts, manifest, result)
}

// registerEngines registers engine metadata from manifest.
func (i *Importer) registerEngines(ctx context.Context, opts *ImportOptions, manifest *PackageManifest, result *ImportResult) (*ImportResult, error) {
	if len(manifest.Engines) > 0 {
		klog.Infof("Engines to import: %d", len(manifest.Engines))

		// Reject unknown task identifiers up-front so partial DB writes never
		// happen. We validate every engine in the manifest before touching any.
		for _, engine := range manifest.Engines {
			if err := validateModelTasks(engine); err != nil {
				result.Errors = append(result.Errors, err)
				return result, err
			}
		}

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

	user, token := opts.RegistryUser, opts.RegistryPassword

	authConfig := registry.AuthConfig{
		Username:      user,
		Password:      token,
		ServerAddress: util.StripRegistryScheme(opts.MirrorRegistry),
	}

	authConfigBytes, err := json.Marshal(authConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal auth config")
	}

	registryAuth := base64.URLEncoding.EncodeToString(authConfigBytes)

	imagePrefix, err := util.BuildImagePrefix(opts.MirrorRegistry, opts.RegistryProject)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build image prefix")
	}

	pushedImages, err := imagePusher.PushImagesToMirrorRegistry(ctx, imagePrefix, registryAuth, manifest)
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
		if opts.MirrorRegistry == "" || opts.RegistryUser == "" || opts.RegistryPassword == "" {
			return errors.New("image registry config is required when not skipping image push")
		}
	}

	return nil
}

// validateModelTasks rejects an EngineMetadata whose top-level or any per-version
// supported_tasks contains an identifier not in v1.IsKnownModelTask. All
// offending values are collected so the user gets one error listing every
// problem instead of fixing them one at a time. Empty / whitespace-only values
// are silently tolerated (aggregateSupportedTasks skips them downstream).
func validateModelTasks(em *EngineMetadata) error {
	if em == nil {
		return nil
	}

	type bad struct {
		where string
		task  string
	}
	var bads []bad

	for _, t := range em.SupportedTasks {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if !v1.IsKnownModelTask(t) {
			bads = append(bads, bad{where: "engines[" + em.Name + "].supported_tasks", task: t})
		}
	}
	for _, v := range em.EngineVersions {
		if v == nil {
			continue
		}
		for _, t := range v.SupportedTasks {
			if strings.TrimSpace(t) == "" {
				continue
			}
			if !v1.IsKnownModelTask(t) {
				bads = append(bads, bad{where: "engines[" + em.Name + "].engine_versions[" + v.Version + "].supported_tasks", task: t})
			}
		}
	}

	if len(bads) == 0 {
		return nil
	}

	parts := make([]string, 0, len(bads))
	for _, b := range bads {
		parts = append(parts, b.where+`=`+strconv.Quote(b.task))
	}
	return fmt.Errorf("unknown model task value(s) — only %q, %q, %q are accepted: %s",
		v1.TextGenerationModelTask, v1.TextEmbeddingModelTask, v1.TextRerankModelTask,
		strings.Join(parts, "; "))
}

// aggregateSupportedTasks unions the manifest top-level supported_tasks with each
// engine_versions[*].supported_tasks. The build script (build-engine-package.sh)
// only emits version-level supported_tasks; the parser does not aggregate; so the
// importer is responsible for producing the engine-level union. Order: top-level
// first, then version-level by version order. Duplicates and empty/whitespace
// entries are skipped while preserving first-occurrence order.
func aggregateSupportedTasks(em *EngineMetadata) []string {
	if em == nil {
		return nil
	}
	var out []string
	out = unionStrings(out, em.SupportedTasks)
	for _, v := range em.EngineVersions {
		if v == nil {
			continue
		}
		out = unionStrings(out, v.SupportedTasks)
	}
	return out
}

// unionStrings returns existing extended with each non-empty trimmed entry of
// incoming that is not already present. existing's order is preserved; new
// entries are appended in incoming order. Returns nil only when both inputs
// produce no kept entries.
func unionStrings(existing, incoming []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := existing
	for _, s := range existing {
		seen[s] = struct{}{}
	}
	for _, s := range incoming {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
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
			SupportedTasks: aggregateSupportedTasks(engineMetadata),
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
			if oldVersion.Version == newVersion.Version {
				found = true
				// merge versions
				if opts.Force {
					existedEngine.Spec.Versions[idx] = util.MergeEngineVersion(oldVersion, newVersion)
				}

				break
			}
		}

		if !found {
			existedEngine.Spec.Versions = append(existedEngine.Spec.Versions, newVersion)
		}
	}

	// Union newly imported tasks into the existing engine's SupportedTasks.
	// We never drop tasks already on the existing engine (NEU-427): users may
	// have set them via prior imports or hand-patched the resource.
	existedEngine.Spec.SupportedTasks = unionStrings(
		existedEngine.Spec.SupportedTasks,
		aggregateSupportedTasks(engineMetadata),
	)

	return i.apiClient.Engines.Update(existedEngine.GetID(), existedEngine)
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
	if isManifestFile(packagePath) {
		return v.validateManifestFile(packagePath)
	}

	return v.validateArchive(packagePath)
}

// validateManifestFile validates a standalone manifest YAML file.
func (v *Validator) validateManifestFile(manifestPath string) error {
	_, err := v.parser.ParseManifestFile(manifestPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse manifest file")
	}

	klog.Info("Manifest validation successful")

	return nil
}

// validateArchive validates a tar.gz archive package.
func (v *Validator) validateArchive(packagePath string) error {
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
