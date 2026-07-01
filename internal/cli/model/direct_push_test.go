package model

import (
	"bytes"
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
	}, tmpDir, DirectPushValidationOptions{})
	require.NoError(t, err)

	err = ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  "https://huggingface.co",
		},
	}, tmpDir, DirectPushValidationOptions{})
	require.ErrorContains(t, err, "only supports bentoml registries backed by nfs://")

	err = ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "file://localhost/tmp/models",
		},
	}, tmpDir, DirectPushValidationOptions{})
	require.ErrorContains(t, err, "only supports bentoml registries backed by nfs://")
}

func TestValidateDirectPushTargetRequiresWritableDirectory(t *testing.T) {
	err := ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, filepath.Join(t.TempDir(), "missing"), DirectPushValidationOptions{})
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
	}, tmpDir, DirectPushValidationOptions{})
	require.ErrorContains(t, err, "is not mounted from server:/exports/models")

	stubMountSource(t, tmpDir, "", false)
	err = ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, tmpDir, DirectPushValidationOptions{})
	require.ErrorContains(t, err, "is not an NFS mount point")
}

func TestValidateDirectPushTargetCanSkipMountCheckForNFSServerPath(t *testing.T) {
	tmpDir := t.TempDir()
	mountCheckCalled := false

	original := getMountSource
	getMountSource = func(localPath string) (string, bool, error) {
		mountCheckCalled = true
		return "", false, nil
	}
	t.Cleanup(func() {
		getMountSource = original
	})

	err := ValidateDirectPushTarget(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "nfs://server:/exports/models",
		},
	}, tmpDir, DirectPushValidationOptions{SkipMountCheck: true})
	require.NoError(t, err)
	require.False(t, mountCheckCalled)
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

func TestImportArchiveToLocalNFSWritesProgress(t *testing.T) {
	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "weights.bin"), []byte("weights"), 0o644))

	archivePath, err := bentoml.CreateArchiveWithProgress(srcDir, "demo-model", "v1", nil)
	require.NoError(t, err)
	defer os.Remove(archivePath)

	archiveInfo, err := os.Stat(archivePath)
	require.NoError(t, err)

	var progress bytes.Buffer
	destDir := t.TempDir()
	require.NoError(t, ImportArchiveToLocalNFS(destDir, archivePath, "demo-model", "v1", &progress))

	require.Equal(t, archiveInfo.Size(), int64(progress.Len()))
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
