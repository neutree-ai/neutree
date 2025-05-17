package bentoml

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
)

type Model struct {
	Tag          string `json:"tag"`
	Module       string `json:"module"`
	Size         string `json:"size"`
	CreationTime string `json:"creation_time"`
}

// GetModelDetail gets detailed information about a specific model
func GetModelDetail(homePath, modelName, version string) (*Model, error) {
	tag := modelName
	if version != "" {
		tag = fmt.Sprintf("%s:%s", modelName, version)
	}

	cmd := exec.Command("bentoml", "models", "get", tag, "-o", "json")
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+homePath)
	content, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get model detail: %s", string(content))
	}

	var model Model
	err = json.Unmarshal(content, &model)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal model detail")
	}

	return &model, nil
}

// DeleteModel deletes a model from BentoML store
func DeleteModel(homePath, modelName, version string) error {
	tag := modelName
	if version != "" {
		tag = fmt.Sprintf("%s:%s", modelName, version)
	}

	cmd := exec.Command("bentoml", "models", "delete", tag, "-y")
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+homePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to delete model: %s", string(output))
	}

	return nil
}

// ImportModel imports a model file to BentoML store
func ImportModel(homePath, modelPath string) error {
	cmd := exec.Command("bentoml", "models", "import", modelPath)
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+homePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to import model: %s", string(output))
	}

	return nil
}

// ExportModel exports a model from BentoML store to a file
func ExportModel(homePath, modelName, version, outputPath string) error {
	tag := modelName
	if version != "" {
		tag = fmt.Sprintf("%s:%s", modelName, version)
	}

	cmd := exec.Command("bentoml", "models", "export", tag, outputPath)
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+homePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to export model: %s", string(output))
	}

	return nil
}

// GetModelPath returns the path where a model is stored in BentoML home
func GetModelPath(homePath, modelName, version string) (string, error) {
	// Get model details to ensure it exists
	model, err := GetModelDetail(homePath, modelName, version)
	if err != nil {
		return "", err
	}

	// Parse the tag to get the actual version
	parts := strings.Split(model.Tag, ":")
	if len(parts) != 2 {
		return "", errors.New("invalid model tag format")
	}
	actualVersion := parts[1]

	// Construct the path to the model directory
	modelDir := filepath.Join(homePath, "models", modelName, actualVersion)
	if _, err := os.Stat(modelDir); os.IsNotExist(err) {
		return "", errors.New("model directory not found")
	}

	return modelDir, nil
}

// ListModels lists all models in BentoML store
func ListModels(homePath string) ([]Model, error) {
	var (
		err           error
		bentoMLModels []Model
	)

	cmd := exec.Command("bentoml", "models", "list", "-o", "json")
	cmd.Env = append(cmd.Env, "BENTOML_HOME="+homePath)

	content, err := cmd.CombinedOutput()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list models: %s", string(content))
	}

	err = json.Unmarshal(content, &bentoMLModels)
	if err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal models list")
	}

	return bentoMLModels, nil
}

// CopyModelFile copies a model file to a temporary location
func CopyModelFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return errors.Wrap(err, "failed to open source file")
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return errors.Wrap(err, "failed to create destination file")
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return errors.Wrap(err, "failed to copy file content")
	}

	return nil
}
