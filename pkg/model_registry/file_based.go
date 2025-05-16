package model_registry

import (
	"net/url"
	"os"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/nfs"
	"github.com/neutree-ai/neutree/pkg/model_registry/bentoml"
)

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
	// todo set list options
	return bentoml.ListModels(f.path)
}

func (f *localFile) HealthyCheck() bool {
	return true
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
	// todo set list options
	return bentoml.ListModels(n.targetPath)
}

func (n *nfsFile) HealthyCheck() bool {
	if _, err := os.Stat(n.targetPath); err != nil {
		return false
	}

	return true
}

func newFileBased(registry *v1.ModelRegistry) (ModelRegistry, error) {
	modelRegistryURL, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return nil, err
	}

	switch modelRegistryURL.Scheme {
	case v1.BentoMLModelRegistryConnectTypeFile:
		return &localFile{
			path: modelRegistryURL.Path,
		}, nil
	case v1.BentoMLModelRegistryConnectTypeNFS:
		return &nfsFile{
			targetPath:    "/mnt/" + registry.Key(),
			nfsServerPath: modelRegistryURL.Host + modelRegistryURL.Path,
		}, nil
	default:
		return nil, errors.New("unsupported model registry protocol: " + modelRegistryURL.Scheme)
	}
}
