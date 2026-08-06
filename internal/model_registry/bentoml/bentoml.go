package bentoml

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
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

	"github.com/docker/go-units"
	"github.com/google/uuid"
	"github.com/klauspost/pgzip"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type Model struct {
	Tag          string            `json:"tag"`
	Module       string            `json:"module"`
	Size         string            `json:"size"`
	CreationTime string            `json:"creation_time"`
	Labels       map[string]string `json:"labels,omitempty"`
}

const (
	ModelYAMLFileName = "model.yaml"
	// ModelJSONFileName is the JSON spelling of the same descriptor; a store may
	// hold either.
	ModelJSONFileName = "model.json"
)

// GetModelDetail gets detailed information about a specific model.
//
// It decodes the whole model.yaml. An earlier narrower struct dropped everything
// but name/version/module/size/creation_time, which meant the labels and
// metadata a user writes into model.yaml — and which the export/import round
// trip faithfully preserves — could not be read back anywhere.
func GetModelDetail(homePath, modelName, version string) (*ModelYAML, error) {
	actualVersion := version

	if version == v1.LatestVersion || version == "" {
		latestPath := filepath.Join(homePath, "models", modelName, v1.LatestVersion)

		data, err := os.ReadFile(latestPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, errors.Errorf("model %s not found", modelName)
			}

			return nil, errors.Wrap(err, "failed to read latest version file")
		}

		actualVersion = strings.TrimSpace(string(data))
	}

	modelDir := filepath.Join(homePath, "models", modelName, actualVersion)
	yamlPath := filepath.Join(modelDir, ModelYAMLFileName)

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.Errorf("model %s:%s not found", modelName, actualVersion)
		}

		return nil, errors.Wrap(err, "failed to read model.yaml")
	}

	var meta ModelYAML
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal model.yaml")
	}

	return &meta, nil
}

// ModelDir returns the directory a model version's files live in. Both segments
// are lowercased, matching the layout ImportModel writes.
func ModelDir(homePath, modelName, version string) string {
	return filepath.Join(homePath, "models", strings.ToLower(modelName), strings.ToLower(version))
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
	meta, err := GetModelDetail(homePath, modelName, version)
	if err != nil {
		return "", err
	}

	// Construct the path to the model directory
	modelDir := ModelDir(homePath, meta.Name, meta.Version)
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
		if name != ModelYAMLFileName && name != ModelJSONFileName {
			return nil
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var meta ModelYAML

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
			Labels:       meta.Labels,
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

// ListModelsWithContext returns when either ListModels completes or ctx expires.
// The underlying filesystem operation cannot be cancelled after it enters the
// kernel, so its result is discarded when the caller's context is done.
func ListModelsWithContext(ctx context.Context, homePath string) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Wrapf(err, "list models at path %s", homePath)
	}

	type result struct {
		models []Model
		err    error
	}

	resultCh := make(chan result, 1)
	go func() {
		models, err := ListModels(homePath)
		resultCh <- result{models: models, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.models, result.err
	case <-ctx.Done():
		return nil, errors.Wrapf(ctx.Err(), "list models at path %s", homePath)
	}
}

// ListModelsWithTimeout applies timeout to ListModelsWithContext.
func ListModelsWithTimeout(homePath string, timeout time.Duration) ([]Model, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return ListModelsWithContext(ctx, homePath)
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

// CalculateDirectorySize sums the size of every regular file under dir. It is
// the only honest way to state what a model occupies: the size recorded in
// model.yaml is a human-readable string baked in at push time, never recomputed,
// and measured over a different set of files than the archive contains.
func CalculateDirectorySize(dir string) (int64, error) {
	var totalSize int64

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			totalSize += info.Size()
		}

		return nil
	})

	return totalSize, err
}

func CreateArchiveWithProgress(srcDir, modelName, version string, progressWriter io.Writer) (string, error) {
	dirSize, err := CalculateDirectorySize(srcDir)
	if err != nil {
		return "", errors.Wrap(err, "failed to calculate directory size")
	}

	yamlPath := filepath.Join(srcDir, ModelYAMLFileName)
	var yamlBytes []byte

	if data, err := os.ReadFile(yamlPath); err == nil {
		var y ModelYAML
		if err := yaml.Unmarshal(data, &y); err == nil {
			now := time.Now().UTC()
			micro := now.Nanosecond() / 1e3
			y.CreationTime = fmt.Sprintf("%s.%06d+00:00",
				now.Format("2006-01-02T15:04:05"), micro)
			y.Size = units.HumanSize(float64(dirSize))
			y.Name = modelName
			y.Version = version
			yamlBytes, _ = yaml.Marshal(&y)
		} else {
			yamlBytes = data
		}
	} else if os.IsNotExist(err) {
		var y ModelYAML
		if err := FillMinimalModelYAML(&y, modelName, version, modelName, dirSize); err != nil {
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

	// Collect checksums: relPath -> sha256 hex (computed while streaming)
	checksums := make(map[string]string)

	// Add model.yaml (content may have been modified, hash the actual bytes written)
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

	yamlHash := sha256.Sum256(yamlBytes)
	checksums[ModelYAMLFileName] = hex.EncodeToString(yamlHash[:])

	// Update progress for yaml file
	if progressWriter != nil {
		_, _ = progressWriter.Write(make([]byte, len(yamlBytes)))
	}

	// Pre-allocate buffer for better performance - reuse across files
	buf := make([]byte, 16*1024*1024)

	// Add all files with progress, computing SHA256 inline via TeeReader
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, _ := filepath.Rel(srcDir, path)
		if rel == "." || rel == ModelYAMLFileName {
			return nil
		}

		// Skip HuggingFace download cache directory — not part of model content.
		if rel == ".cache" || strings.HasPrefix(rel, ".cache"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}

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

		// Chain TeeReaders: file -> progress + sha256 hash -> tar writer
		h := sha256.New()

		var reader io.Reader = f
		if progressWriter != nil {
			reader = io.TeeReader(reader, progressWriter)
		}

		reader = io.TeeReader(reader, h)

		_, err = io.CopyBuffer(tw, reader, buf)
		if err != nil {
			return err
		}

		checksums[rel] = hex.EncodeToString(h.Sum(nil))

		return nil
	})

	if err != nil {
		return "", err
	}

	// Append .neutree/checksums/ entries to the archive
	if err = writeChecksumsToTar(tw, checksums); err != nil {
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

// ModelYAML is a model.yaml. The JSON tags matter as much as the YAML ones:
// ListModels also reads model.json, and without them a snake_case key such as
// creation_time would not bind.
type ModelYAML struct {
	Name       string                 `yaml:"name" json:"name"`
	Version    string                 `yaml:"version" json:"version"`
	Module     string                 `yaml:"module" json:"module"`
	Size       string                 `yaml:"size,omitempty" json:"size,omitempty"`
	APIVersion string                 `yaml:"api_version" json:"api_version"`
	Signatures map[string]interface{} `yaml:"signatures" json:"signatures"`
	Labels     map[string]string      `yaml:"labels" json:"labels"`
	Options    map[string]interface{} `yaml:"options" json:"options"`
	Metadata   map[string]interface{} `yaml:"metadata" json:"metadata"`
	Context    struct {               // nested
		FrameworkName    string            `yaml:"framework_name" json:"framework_name"`
		FrameworkVersion map[string]string `yaml:"framework_versions" json:"framework_versions"`
		BentoVersion     string            `yaml:"bentoml_version" json:"bentoml_version"`
		PythonVersion    string            `yaml:"python_version" json:"python_version"`
	} `yaml:"context" json:"context"`
	CreationTime string `yaml:"creation_time" json:"creation_time"`
}

func FillMinimalModelYAML(y *ModelYAML, name, version, hfRepo string, size int64) error {
	*y = ModelYAML{
		Name:       name,
		Version:    version,
		Module:     "",
		Size:       units.HumanSize(float64(size)),
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

// checksumRecord is the JSON structure written to .neutree/checksums/<file>.json.
type checksumRecord struct {
	Algorithm string `json:"algorithm"`
	Hash      string `json:"hash"`
}

// writeChecksumsToTar appends .neutree/checksums/<relpath>.json entries to a tar writer.
func writeChecksumsToTar(tw *tar.Writer, checksums map[string]string) error {
	for relPath, hash := range checksums {
		record := checksumRecord{Algorithm: "sha256", Hash: hash}
		data, _ := json.Marshal(record)

		entryName := filepath.Join(".neutree", "checksums", relPath+".json")
		hdr := &tar.Header{
			Name:     entryName,
			Mode:     0o644,
			Size:     int64(len(data)),
			ModTime:  time.Now(),
			Typeflag: tar.TypeReg,
		}

		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if _, err := tw.Write(data); err != nil {
			return err
		}
	}

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
