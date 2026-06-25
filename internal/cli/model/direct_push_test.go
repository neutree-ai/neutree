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
	stubMountSource(t, tmpDir, "server:/exports/models", true)

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

func TestValidateDirectPushTargetRequiresMatchingNFSMount(t *testing.T) {
	tmpDir := t.TempDir()
	stubMountSource(t, tmpDir, "server:/other", true)

	err := ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, tmpDir)
	require.ErrorContains(t, err, "is not mounted from server:/exports/models")

	stubMountSource(t, tmpDir, "", false)
	err = ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, tmpDir)
	require.ErrorContains(t, err, "is not an NFS mount point")
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

func TestReadImportedModelVersionReturnsLocalMetadata(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "weights.bin"), []byte("weights"), 0o644))

	archivePath, err := bentoml.CreateArchiveWithProgress(srcDir, "Demo-Model", "V1", nil)
	require.NoError(t, err)
	defer os.Remove(archivePath)

	destDir := t.TempDir()
	require.NoError(t, ImportArchiveToLocalNFS(destDir, archivePath, "Demo-Model", "V1", nil))

	version, err := ReadImportedModelVersion(destDir, "Demo-Model", "V1")
	require.NoError(t, err)
	require.Equal(t, "V1", version.Name)
	require.NotEmpty(t, version.CreationTime)
	require.NotEmpty(t, version.Size)
}

func stubMountSource(t *testing.T, path, source string, mounted bool) {
	t.Helper()

	original := getMountSource
	getMountSource = func(localPath string) (string, bool, error) {
		require.Equal(t, path, localPath)
		return source, mounted, nil
	}
	t.Cleanup(func() {
		getMountSource = original
	})
}
