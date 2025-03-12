package bentoml

import (
	"encoding/json"
	"os/exec"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type Model struct {
	Tag          string `json:"tag"`
	Module       string `json:"module"`
	Size         string `json:"size"`
	CreationTime string `json:"creation_time"`
}

func ListModels(homePath string) ([]v1.GeneralModel, error) {
	var (
		err           error
		bentoMLModels []Model
		generalModels []v1.GeneralModel
	)
	cmd := exec.Command("bentoml", "models", "list", "-o", "json")
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+homePath)

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
