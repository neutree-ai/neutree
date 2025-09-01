package bentoml

import (
	"archive/tar"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"slices"

	"github.com/google/uuid"
	"github.com/klauspost/pgzip"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

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

// ImportModel imports a model from reader to BentoML store
func ImportModel(homePath string, reader io.Reader, name, version string, force bool, progress io.Writer) error {
	if name == "" || version == "" {
		return errors.New("model name and version are required")
	}

	lname := strings.ToLower(name)
	lversion := strings.ToLower(version)
	modelDir := filepath.Join(homePath, "models", lname)
	finalDir := filepath.Join(modelDir, lversion)

	if exists(finalDir) {
		if !force {
			return errors.Errorf("model %s:%s already exists", lname, lversion)
		}
		// Remove old version to replace.
		if err := os.RemoveAll(finalDir); err != nil {
			return errors.Wrap(err, "cleanup existing model dir")
		}
	}

	// Step 1: create temporary directory for atomic import.
	tmpParent := filepath.Dir(finalDir)
	if err := os.MkdirAll(tmpParent, 0o755); err != nil {
		return errors.Wrap(err, "mkdir model parent")
	}

	tmpDir, err := os.MkdirTemp(tmpParent, ".tmp-*-")
	if err != nil {
		return errors.Wrap(err, "create temp dir")
	}
	// Ensure tmpDir removed on failure.
	defer func() {
		if err != nil {
			os.RemoveAll(tmpDir)
		}
	}()

	// Step 2: extract archive from reader into tmpDir.
	if err = untarGzFromReader(reader, tmpDir, progress); err != nil {
		return err
	}

	// Step 3: atomic rename to final destination.
	if err = os.Rename(tmpDir, finalDir); err != nil {
		return errors.Wrap(err, "atomic rename")
	}

	// Step 4: update version dir mode
	if err = os.Chmod(finalDir, 0o755); err != nil {
		return errors.Wrap(err, "chmod model dir")
	}

	// Step 5: recreate latest tag
	if err = recreateLatestTag(modelDir, lversion); err != nil {
		return errors.Wrapf(err, "failed to recreate latest tag")
	}

	return nil
}

func recreateLatestTag(modelDir, version string) error {
	latestFilePath := filepath.Join(modelDir, v1.LatestVersion)

	latestFile, err := os.OpenFile(latestFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return errors.Wrap(err, "create latest file")
	}
	defer latestFile.Close()

	_, err = latestFile.WriteString(version)
	if err != nil {
		return errors.Wrap(err, "write latest file")
	}

	if err := latestFile.Sync(); err != nil {
		return errors.Wrap(err, "sync latest file to disk")
	}

	return nil
}

// exists returns true if path exists.
func exists(p string) bool { _, err := os.Stat(p); return err == nil }

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

// ListModels traverses $homePath/bentoml/models and aggregates model.yaml /
// model.json files.  It is API‑compatible with the original CLI‑based ListModels
// but avoids forking Python.
func ListModels(homePath string) ([]Model, error) {
	root := filepath.Join(homePath, "models")

	stat, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			klog.Warningf("Model store %s not found, returning empty list", root)
			return []Model{}, nil
		}

		return nil, errors.Wrap(err, "model store not found")
	}

	if !stat.IsDir() {
		return nil, errors.Errorf("%s is not a directory", root)
	}

	var models []Model

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		name := d.Name()
		if name != "model.yaml" && name != "model.json" {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Minimal superset of fields we care about
		var meta struct {
			Name         string `yaml:"name" json:"name"`
			Version      string `yaml:"version" json:"version"`
			Module       string `yaml:"module" json:"module"`
			Size         string `yaml:"size" json:"size"`
			CreationTime string `yaml:"creation_time" json:"creation_time"`
		}

		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			if err := yaml.Unmarshal(raw, &meta); err != nil {
				return err
			}
		} else {
			if err := json.Unmarshal(raw, &meta); err != nil {
				return err
			}
		}

		models = append(models, Model{
			Tag:          fmt.Sprintf("%s:%s", strings.ToLower(meta.Name), strings.ToLower(meta.Version)),
			Module:       meta.Module,
			Size:         meta.Size,
			CreationTime: meta.CreationTime,
		})

		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// 2) Sort by CreationTime desc (same as BentoML CLI output)
	sort.Slice(models, func(i, j int) bool {
		return models[i].CreationTime > models[j].CreationTime
	})

	return models, nil
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

	gzw, err := pgzip.NewWriterLevel(tmpFile, pgzip.BestSpeed)
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
		_, _ = progressWriter.Write(make([]byte, len(yamlBytes)))
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

func untarGzFromReader(reader io.Reader, dest string, progressWriter io.Writer) error {
	gr, err := pgzip.NewReader(reader)
	if err != nil {
		return errors.Wrap(err, "pgzip reader")
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	buf := make([]byte, 16*1024*1024)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return errors.Wrap(err, "iterate tar")
		}

		clean := filepath.Clean(hdr.Name)
		if clean == "." || strings.Contains(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe path %q in archive", hdr.Name)
		}

		target := filepath.Join(dest, clean)

		switch hdr.Typeflag {
		case tar.TypeDir:
			mode := fs.FileMode(hdr.Mode & 0o777)
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}

			mode := fs.FileMode(hdr.Mode & 0o777)

			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}

			var r io.Reader = tr
			if progressWriter != nil {
				r = io.TeeReader(tr, progressWriter)
			}

			if _, err := io.CopyBuffer(out, r, buf); err != nil {
				out.Close()
				return err
			}

			out.Close()

			_ = os.Chtimes(target, time.Now(), hdr.ModTime)
		default:
			// skip other types (symlink, etc) for safety
		}
	}

	return nil
}
