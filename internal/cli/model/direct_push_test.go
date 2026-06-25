package model

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/bentoml"
)

func TestValidateDirectPushTargetRequiresBentomlNFSRegistry(t *testing.T) {
	tmpDir := t.TempDir()

	err := ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, tmpDir)
	require.NoError(t, err)

	err = ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
	}, tmpDir)
	require.ErrorContains(t, err, "only supports bentoml registries backed by nfs://")

	err = ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "file://localhost/tmp/models",
		},
	}, tmpDir)
	require.ErrorContains(t, err, "only supports bentoml registries backed by nfs://")
}

func TestValidateDirectPushTargetRequiresWritableDirectory(t *testing.T) {
	err := ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "local NFS path")
}

func TestImportArchiveToLocalNFSUsesBentoMLLayout(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "weights.bin"), []byte("weights"), 0o644))

	archivePath, err := bentoml.CreateArchiveWithProgress(srcDir, "demo-model", "v1", nil)
	require.NoError(t, err)
	defer os.Remove(archivePath)

	destDir := t.TempDir()
	require.NoError(t, ImportArchiveToLocalNFS(destDir, archivePath, "demo-model", "v1", nil))

	require.FileExists(t, filepath.Join(destDir, "models", "demo-model", "v1", "model.yaml"))
	require.FileExists(t, filepath.Join(destDir, "models", "demo-model", "latest"))
}
