package model_registry

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/bentoml"
	"github.com/neutree-ai/neutree/internal/model_registry/modelmeta"
	"github.com/neutree-ai/neutree/internal/nfs"
)

// readmeFileName is the model card a private registry serves verbatim.
const readmeFileName = "README.md"

const nfsHealthCheckTimeout = time.Minute

func convertBentoMLModelsToGeneralModels(bentomlModels []bentoml.Model, options ListOption) *ModelPage {
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
			Labels:       model.Labels,
		})
	}

	// Convert map to slice
	generalModels := make([]v1.GeneralModel, 0, len(generalModelMap))
	for _, model := range generalModelMap {
		generalModels = append(generalModels, *model)
	}

	// Go randomises map iteration order, so the slice above comes out in a
	// different order on every call. Sort by name before paging: without a
	// stable order, offset/limit hand back overlapping pages that silently drop
	// models, and even a bare limit returns a different subset each request.
	sort.Slice(generalModels, func(i, j int) bool {
		return generalModels[i].Name < generalModels[j].Name
	})

	return pageOf(generalModels, options)
}

// pageOf applies Offset and Limit. An offset past the end is an empty page, not
// an error — a client paging a registry that shrank under it should see the end
// of the list, not a failure.
func pageOf(models []v1.GeneralModel, options ListOption) *ModelPage {
	page := &ModelPage{Total: KnownTotal(len(models))}

	start := options.Offset
	if start < 0 {
		start = 0
	}

	if start > len(models) {
		start = len(models)
	}

	end := len(models)
	if options.Limit > 0 && start+options.Limit < end {
		end = start + options.Limit
	}

	page.Models = models[start:end]

	return page
}

// bentomlStore is the BentoML-layout model tree both file-based registries read:
// the local one addresses it directly, the NFS one through its mount point.
type bentomlStore struct {
	path string
}

func (s *bentomlStore) ListModels(options ListOption) (*ModelPage, error) {
	// Get BentoML models
	bentomlModels, err := bentoml.ListModels(s.path)
	if err != nil {
		return nil, err
	}

	// Convert to GeneralModel format using the helper function
	return convertBentoMLModelsToGeneralModels(bentomlModels, options), nil
}

func (s *bentomlStore) GetModelVersion(name, version string) (*v1.ModelVersion, error) {
	model, err := bentoml.GetModelDetail(s.path, name, version)
	if err != nil {
		return nil, err
	}

	return modelVersionFromYAML(model), nil
}

func (s *bentomlStore) GetModelDetail(name, version string) (*v1.ModelVersion, error) {
	model, err := bentoml.GetModelDetail(s.path, name, version)
	if err != nil {
		return nil, err
	}

	detail := modelVersionFromYAML(model)
	detail.Info = modelmeta.Parse(bentoml.ModelDir(s.path, model.Name, model.Version))

	manual, err := bentoml.ReadManualModelInfo(model.Metadata)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read hand-filled model info of %s:%s", name, version)
	}

	detail.Info.MergeManual(manual)

	return detail, nil
}

func (s *bentomlStore) GetReadme(name, version string) (*Readme, error) {
	model, err := bentoml.GetModelDetail(s.path, name, version)
	if err != nil {
		return nil, err
	}

	readmePath := filepath.Join(bentoml.ModelDir(s.path, model.Name, model.Version), readmeFileName)

	file, err := os.Open(readmePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.Wrapf(ErrNotFound, "model %s:%s has no %s", name, version, readmeFileName)
		}

		return nil, errors.Wrapf(err, "failed to read %s of model %s:%s", readmeFileName, name, version)
	}
	defer file.Close()

	readme, err := readCappedReadme(file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s of model %s:%s", readmeFileName, name, version)
	}

	return readme, nil
}

// readCappedReadme reads at most MaxReadmeBytes, asking for one byte more so it
// can tell "exactly at the limit" from "longer than the limit" without reading
// the rest.
func readCappedReadme(source io.Reader) (*Readme, error) {
	content, err := io.ReadAll(io.LimitReader(source, MaxReadmeBytes+1))
	if err != nil {
		return nil, err
	}

	if len(content) > MaxReadmeBytes {
		return &Readme{Content: string(truncateUTF8(content, MaxReadmeBytes)), Truncated: true}, nil
	}

	return &Readme{Content: string(content)}, nil
}

// truncateUTF8 cuts at or before limit without splitting a rune, so the result
// is still text. Markdown cut mid-document is expected here; markdown cut
// mid-character is a decoding error in whatever displays it.
func truncateUTF8(content []byte, limit int) []byte {
	if len(content) <= limit {
		return content
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}

	return content[:cut]
}

func (s *bentomlStore) DeleteModel(name, version string) error {
	return bentoml.DeleteModel(s.path, name, version)
}

func (s *bentomlStore) ImportModel(reader io.Reader, name, version string, progress io.Writer) error {
	return bentoml.ImportModel(s.path, reader, name, version, true, progress)
}

func (s *bentomlStore) ExportModel(name, version, outputPath string) error {
	return bentoml.ExportModel(s.path, name, version, outputPath)
}

func (s *bentomlStore) GetModelPath(name, version string) (string, error) {
	return bentoml.GetModelPath(s.path, name, version)
}

func (s *bentomlStore) CollectUsage() (*RegistryUsage, error) {
	usage, err := bentoml.CollectUsage(s.path)
	if err != nil {
		return nil, err
	}

	return &RegistryUsage{ModelCount: usage.ModelCount, StorageBytes: usage.StorageBytes}, nil
}

// SetManualModelInfo records the model info fields a user filled in by hand. The
// block is replaced wholesale, so the caller sends the complete set of overrides
// it wants kept.
func (s *bentomlStore) SetManualModelInfo(name, version string, info *v1.ModelInfo) error {
	return bentoml.WriteManualModelInfo(s.path, name, version, info)
}

// modelVersionFromYAML projects a model.yaml onto the API shape. Labels come
// along: they are what a user writes into model.yaml, and until now both read
// paths decoded into a struct that did not have them, so everything written
// there was silently unreadable.
func modelVersionFromYAML(model *bentoml.ModelYAML) *v1.ModelVersion {
	return &v1.ModelVersion{
		Name:         model.Version,
		CreationTime: model.CreationTime,
		Size:         model.Size,
		Module:       model.Module,
		Labels:       model.Labels,
	}
}

// todo: Current model repository for local file types has not yet been fully designed
// and will be improved after the design is completed.
type localFile struct {
	bentomlStore
}

func (f *localFile) Connect() error {
	return nil
}

func (f *localFile) Disconnect() error {
	return nil
}

func (f *localFile) HealthyCheck() error {
	// Try to list models to verify functionality
	if _, err := bentoml.ListModels(f.path); err != nil {
		return errors.Wrapf(err, "failed to list models at path %s", f.path)
	}

	return nil
}

func (f *localFile) GetNFSVersion() (string, error) {
	return "", nil
}

type nfsFile struct {
	bentomlStore

	nfsServerPath string
}

func (n *nfsFile) Connect() error {
	return nfs.MountNFS(n.nfsServerPath, n.path)
}

func (n *nfsFile) Disconnect() error {
	// Disconnect intentionally keeps nfs.Unmount's synchronous behavior. A
	// ListModels timeout has already made the registry Failed; unmount retains
	// its lifecycle semantics before the controller reconnects.
	return nfs.Unmount(n.path)
}

func (n *nfsFile) HealthyCheck() error {
	exists, err := nfs.IsMountExist(n.nfsServerPath, n.path)
	if err != nil {
		return errors.Wrapf(err, "failed to check NFS mount %s to %s", n.nfsServerPath, n.path)
	}

	if !exists {
		return errors.Errorf("NFS mount %s to %s does not exist", n.nfsServerPath, n.path)
	}

	if _, err := bentoml.ListModelsWithTimeout(n.path, nfsHealthCheckTimeout); err != nil {
		return errors.Wrapf(err, "failed to list models at NFS path %s", n.path)
	}

	return nil
}

func (n *nfsFile) GetNFSVersion() (string, error) {
	return nfs.GetNFSVersion(n.nfsServerPath, n.path)
}

func newFileBased(registry *v1.ModelRegistry) (ModelRegistry, error) {
	modelRegistryURL, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return nil, err
	}

	switch modelRegistryURL.Scheme {
	case string(v1.BentoMLModelRegistryConnectTypeFile):
		if modelRegistryURL.Path == "" {
			return nil, errors.New("file path is required for file-based model registry")
		}

		return &localFile{
			bentomlStore: bentomlStore{path: modelRegistryURL.Path},
		}, nil
	case string(v1.BentoMLModelRegistryConnectTypeNFS):
		if modelRegistryURL.Host == "" || modelRegistryURL.Path == "" {
			return nil, errors.New("both NFS server and path are required for NFS-based model registry")
		}

		return &nfsFile{
			bentomlStore:  bentomlStore{path: filepath.Join("/mnt", registry.Key())},
			nfsServerPath: modelRegistryURL.Host + modelRegistryURL.Path,
		}, nil
	default:
		return nil, errors.New("unsupported model registry protocol: " + modelRegistryURL.Scheme)
	}
}
