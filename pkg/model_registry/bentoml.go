package model_registry

import (
	"encoding/json"
	"net/url"
	"os/exec"
	"strings"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/utils"
)

const (
	localBentomlPath = "/home/bentoml/bento/models"
)

type fileBentoML struct {
	path string
}

func (f *fileBentoML) Connect() error {
	return nil
}

func (f *fileBentoML) Disconnect() error {
	return nil
}

func (f *fileBentoML) ListModels(options ListOption) ([]v1.GeneralModel, error) {
	// todo set list options
	return listBentoMLModels(f.path)
}

func (f *fileBentoML) HealthyCheck() bool {
	return true
}

type nfsBentoML struct {
	targetPath    string
	nfsServerPath string
}

func (n *nfsBentoML) Connect() error {
	return utils.MountNFS(n.nfsServerPath, n.targetPath)
}

func (n *nfsBentoML) Disconnect() error {
	return utils.Unmount(n.targetPath)
}

func (n *nfsBentoML) ListModels(options ListOption) ([]v1.GeneralModel, error) {
	// todo set list options
	return listBentoMLModels(n.targetPath)
}

func (n *nfsBentoML) HealthyCheck() bool {
	return true
}

func newBentoML(registry *v1.ModelRegistry) (ModelRegistry, error) {
	modelRegistryURL, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return nil, err
	}

	switch modelRegistryURL.Scheme {
	case "file":
		return &fileBentoML{
			path: localBentomlPath,
		}, nil
	case "nfs":
		return &nfsBentoML{
			targetPath:    "/mnt/" + registry.Metadata.Name,
			nfsServerPath: modelRegistryURL.Host + ":" + modelRegistryURL.Path,
		}, nil
	default:
		return nil, errors.New("unsupported model registry protocol: " + modelRegistryURL.Scheme)
	}
}

type BentoMLModel struct {
	Tag          string `json:"tag"`
	Module       string `json:"module"`
	Size         string `json:"size"`
	CreationTime string `json:"creation_time"`
}

func listBentoMLModels(bentoMLHome string) ([]v1.GeneralModel, error) {
	var (
		err           error
		bentoMLModels []BentoMLModel
		generalModels []v1.GeneralModel
	)
	cmd := exec.Command("bentoml", "models", "list", "-o", "json")
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+bentoMLHome)

	content, err := cmd.CombinedOutput()
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(content, &bentoMLModels)
	if err != nil {
		return nil, err
	}

	generalModelMap := make(map[string]*v1.GeneralModel)

	for _, model := range bentoMLModels {
		tmp := strings.Split(model.Tag, ":")
		name, version := tmp[0], tmp[1]

		if _, ok := generalModelMap[name]; !ok {
			generalModelMap[name] = &v1.GeneralModel{
				Name: name,
			}
		}

		generalModelMap[name].Versions = append(generalModelMap[name].Versions, v1.Version{
			Name:         version,
			CreationTime: model.CreationTime,
		})
	}

	for key := range generalModelMap {
		generalModels = append(generalModels, *generalModelMap[key])
	}

	return generalModels, nil
}
