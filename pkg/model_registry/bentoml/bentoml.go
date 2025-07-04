package bentoml

import (
	"archive/tar"
	"compress/gzip"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"slices"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type Model struct {
	Tag          string `json:"tag"`
	Module       string `json:"module"`
	Size         string `json:"size"`
	CreationTime string `json:"creation_time"`
}

const (
	ModelYAMLFileName = "model.yaml"
)

// GetModelDetail gets detailed information about a specific model
func GetModelDetail(homePath, modelName, version string) (*Model, error) {
	tag := modelName
	if version != "" {
		tag = fmt.Sprintf("%s:%s", modelName, version)
	}

	cmd := exec.Command("bentoml", "models", "get", tag, "-o", "json")
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", v1.BentoMLHomeEnv, homePath))

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
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", v1.BentoMLHomeEnv, homePath))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return errors.Wrapf(err, "failed to delete model: %s", string(output))
	}

	return nil
}

// ImportModel imports a model file to BentoML store
func ImportModel(homePath, modelPath string) error {
	cmd := exec.Command("bentoml", "models", "import", modelPath)
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", v1.BentoMLHomeEnv, homePath))

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
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", v1.BentoMLHomeEnv, homePath))

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
	cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", v1.BentoMLHomeEnv, homePath))

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

// GenerateVersion returns a 16‑char, lowercase base‑32 string identical to BentoML's default.
func GenerateVersion() (*string, error) {
	u, err := uuid.NewUUID()
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate UUID")
	}

	b := u[:]
	trimmed := slices.Concat(b[:6:6], b[8:12]) // 10 bytes
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(trimmed)
	lower := strings.ToLower(enc)

	return &lower, nil
}

func CreateArchiveWithProgress(srcDir, modelName, version string, progressWriter io.Writer) (string, error) {
	yamlPath := filepath.Join(srcDir, ModelYAMLFileName)
	var yamlBytes []byte

	if data, err := os.ReadFile(yamlPath); err == nil {
		var y ModelYAML
		if err := yaml.Unmarshal(data, &y); err == nil {
			now := time.Now().UTC()
			micro := now.Nanosecond() / 1e3
			y.CreationTime = fmt.Sprintf("%s.%06d+00:00",
				now.Format("2006-01-02T15:04:05"), micro)
			yamlBytes, _ = yaml.Marshal(&y)
		} else {
			yamlBytes = data
		}
	} else if os.IsNotExist(err) {
		var y ModelYAML
		if err := FillMinimalModelYAML(&y, modelName, version, modelName); err != nil {
			return "", err
		}

		yamlBytes, _ = yaml.Marshal(&y)
	} else {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", fmt.Sprintf("%s-%s-*.bentomodel", modelName, version))
	if err != nil {
		return "", err
	}

	defer func() {
		if err != nil {
			tmpFile.Close()
			os.Remove(tmpFile.Name())
		}
	}()

	gzw, err := gzip.NewWriterLevel(tmpFile, gzip.BestSpeed)
	if err != nil {
		return "", err
	}
	tw := tar.NewWriter(gzw)

	// Add model.yaml
	hdr := &tar.Header{
		Name:     ModelYAMLFileName,
		Mode:     0o644,
		Size:     int64(len(yamlBytes)),
		ModTime:  time.Now(),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return "", err
	}

	if _, err := tw.Write(yamlBytes); err != nil {
		return "", err
	}

	// Update progress for yaml file
	if progressWriter != nil {
		progressWriter.Write(make([]byte, len(yamlBytes)))
	}

	// Pre-allocate buffer for better performance - reuse across files
	buf := make([]byte, 16*1024*1024)

	// Add all files with progress
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, _ := filepath.Rel(srcDir, path)
		if rel == "." || rel == ModelYAMLFileName {
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		hdr.Name = rel
		hdr.ModTime = time.Now()

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		// Use io.TeeReader to copy data and update progress simultaneously
		var reader io.Reader = f
		if progressWriter != nil {
			reader = io.TeeReader(f, progressWriter)
		}

		_, err = io.CopyBuffer(tw, reader, buf)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	if err = tw.Close(); err != nil {
		return "", err
	}

	if err = gzw.Close(); err != nil {
		return "", err
	}

	if err = tmpFile.Close(); err != nil {
		return "", err
	}

	return tmpFile.Name(), nil
}

type ModelYAML struct {
	Name       string                 `yaml:"name"`
	Version    string                 `yaml:"version"`
	Module     string                 `yaml:"module"`
	APIVersion string                 `yaml:"api_version"`
	Signatures map[string]interface{} `yaml:"signatures"`
	Labels     map[string]string      `yaml:"labels"`
	Options    map[string]interface{} `yaml:"options"`
	Metadata   map[string]interface{} `yaml:"metadata"`
	Context    struct {               // nested
		FrameworkName    string            `yaml:"framework_name"`
		FrameworkVersion map[string]string `yaml:"framework_versions"`
		BentoVersion     string            `yaml:"bentoml_version"`
		PythonVersion    string            `yaml:"python_version"`
	} `yaml:"context"`
	CreationTime string `yaml:"creation_time"`
}

func FillMinimalModelYAML(y *ModelYAML, name, version, hfRepo string) error {
	*y = ModelYAML{
		Name:       name,
		Version:    version,
		Module:     "",
		APIVersion: "v1",
		Signatures: map[string]interface{}{},
		Labels:     map[string]string{},
		Options:    map[string]interface{}{},
		Metadata:   map[string]interface{}{},
	}

	now := time.Now().UTC()
	micro := now.Nanosecond() / 1e3
	y.CreationTime = fmt.Sprintf("%s.%06d+00:00", now.Format("2006-01-02T15:04:05"), micro)
	y.Context.FrameworkName = "transformers"
	y.Context.FrameworkVersion = map[string]string{}
	y.Context.BentoVersion = "1.4.6"
	y.Context.PythonVersion = "3.12"

	return nil
}
