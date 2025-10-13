package model_registry

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/nfs"
	"github.com/neutree-ai/neutree/pkg/model_registry/bentoml"
)

func convertBentoMLModelsToGeneralModels(bentomlModels []bentoml.Model, options ListOption) []v1.GeneralModel {
	// Convert to GeneralModel format
	generalModelMap := make(map[string]*v1.GeneralModel)

	for _, model := range bentomlModels {
		tmp := strings.Split(model.Tag, ":")
		if len(tmp) != 2 {
			continue
		}

		name, version := tmp[0], tmp[1]

		// Apply search filter if specified
		if options.Search != "" && !strings.Contains(name, options.Search) {
			continue
		}

		// Create GeneralModel entry if not exists
		if _, ok := generalModelMap[name]; !ok {
			generalModelMap[name] = &v1.GeneralModel{
				Name: name,
			}
		}

		// Add version information
		generalModelMap[name].Versions = append(generalModelMap[name].Versions, v1.ModelVersion{
			Name:         version,
			CreationTime: model.CreationTime,
			Size:         model.Size,
			Module:       model.Module,
		})
	}

	// Convert map to slice
	generalModels := make([]v1.GeneralModel, 0, len(generalModelMap))
	for _, model := range generalModelMap {
		generalModels = append(generalModels, *model)
	}

	// Apply limit if specified
	if options.Limit > 0 && len(generalModels) > options.Limit {
		generalModels = generalModels[:options.Limit]
	}

	return generalModels
}

type localFile struct {
	path string
}

func (f *localFile) Connect() error {
	return nil
}

func (f *localFile) Disconnect() error {
	return nil
}

func (f *localFile) ListModels(options ListOption) ([]v1.GeneralModel, error) {
	// Get BentoML models
	bentomlModels, err := bentoml.ListModels(f.path)
	if err != nil {
		return nil, err
	}

	// Convert to GeneralModel format using the helper function
	return convertBentoMLModelsToGeneralModels(bentomlModels, options), nil
}

func (f *localFile) GetModelVersion(name, version string) (*v1.ModelVersion, error) {
	bentomlModel, err := bentoml.GetModelDetail(f.path, name, version)
	if err != nil {
		return nil, err
	}

	// Parse tag to get the actual version
	parts := strings.Split(bentomlModel.Tag, ":")
	modelVersion := version

	if len(parts) == 2 {
		modelVersion = parts[1]
	}

	return &v1.ModelVersion{
		Name:         modelVersion,
		CreationTime: bentomlModel.CreationTime,
		Size:         bentomlModel.Size,
		Module:       bentomlModel.Module,
	}, nil
}

func (f *localFile) DeleteModel(name, version string) error {
	return bentoml.DeleteModel(f.path, name, version)
}

func (f *localFile) ImportModel(reader io.Reader, name, version string, progress io.Writer) error {
	return bentoml.ImportModel(f.path, reader, name, version, true, progress)
}

func (f *localFile) ExportModel(name, version, outputPath string) error {
	return bentoml.ExportModel(f.path, name, version, outputPath)
}

func (f *localFile) GetModelPath(name, version string) (string, error) {
	return bentoml.GetModelPath(f.path, name, version)
}

func (f *localFile) HealthyCheck() error {
	if _, err := os.Stat(f.path); err != nil {
		return errors.Wrapf(err, "failed to access model registry path %s", f.path)
	}

	// Try to list models to verify functionality
	if _, err := bentoml.ListModels(f.path); err != nil {
		return errors.Wrapf(err, "failed to list models at path %s", f.path)
	}

	return nil
}

type nfsFile struct {
	targetPath    string
	nfsServerPath string
}

func (n *nfsFile) Connect() error {
	return nfs.MountNFS(n.nfsServerPath, n.targetPath)
}

func (n *nfsFile) Disconnect() error {
	return nfs.Unmount(n.targetPath)
}

func (n *nfsFile) ListModels(options ListOption) ([]v1.GeneralModel, error) {
	// Get BentoML models
	bentomlModels, err := bentoml.ListModels(n.targetPath)
	if err != nil {
		return nil, err
	}

	// Convert to GeneralModel format using the helper function
	return convertBentoMLModelsToGeneralModels(bentomlModels, options), nil
}

func (n *nfsFile) GetModelVersion(name, version string) (*v1.ModelVersion, error) {
	bentomlModel, err := bentoml.GetModelDetail(n.targetPath, name, version)
	if err != nil {
		return nil, err
	}

	// Parse tag to get the actual version
	parts := strings.Split(bentomlModel.Tag, ":")
	modelVersion := version

	if len(parts) == 2 {
		modelVersion = parts[1]
	}

	return &v1.ModelVersion{
		Name:         modelVersion,
		CreationTime: bentomlModel.CreationTime,
		Size:         bentomlModel.Size,
		Module:       bentomlModel.Module,
	}, nil
}

func (n *nfsFile) DeleteModel(name, version string) error {
	return bentoml.DeleteModel(n.targetPath, name, version)
}

func (n *nfsFile) ImportModel(reader io.Reader, name, version string, progress io.Writer) error {
	return bentoml.ImportModel(n.targetPath, reader, name, version, true, progress)
}

func (n *nfsFile) ExportModel(name, version, outputPath string) error {
	return bentoml.ExportModel(n.targetPath, name, version, outputPath)
}

func (n *nfsFile) GetModelPath(name, version string) (string, error) {
	return bentoml.GetModelPath(n.targetPath, name, version)
}

func (n *nfsFile) HealthyCheck() error {
	if _, err := os.Stat(n.targetPath); err != nil {
		return errors.Wrapf(err, "failed to access NFS mount path %s", n.targetPath)
	}

	// Try to list models to verify functionality
	if _, err := bentoml.ListModels(n.targetPath); err != nil {
		return errors.Wrapf(err, "failed to list models at NFS path %s", n.targetPath)
	}

	return nil
}

func newFileBased(registry *v1.ModelRegistry) (ModelRegistry, error) {
	modelRegistryURL, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return nil, err
	}

	switch modelRegistryURL.Scheme {
	case string(v1.BentoMLModelRegistryConnectTypeFile):
		return &localFile{
			path: modelRegistryURL.Path,
		}, nil
	case string(v1.BentoMLModelRegistryConnectTypeNFS):
		return &nfsFile{
			targetPath:    filepath.Join("/mnt", registry.Key()),
			nfsServerPath: modelRegistryURL.Host + modelRegistryURL.Path,
		}, nil
	default:
		return nil, errors.New("unsupported model registry protocol: " + modelRegistryURL.Scheme)
	}
}
