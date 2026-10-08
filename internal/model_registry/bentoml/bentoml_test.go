package bentoml

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestListModelsWithContextReturnsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	models, err := ListModelsWithContext(ctx, t.TempDir())

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, models)
}

func TestListModelsWithTimeoutReturnsExpiredDeadline(t *testing.T) {
	models, err := ListModelsWithTimeout(t.TempDir(), 0)

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, models)
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestCreateArchiveWithProgressChecksums(t *testing.T) {
	t.Run("archive contains correct checksums", func(t *testing.T) {
		srcDir := t.TempDir()

		weightsContent := []byte("fake-weights-data")
		configContent := []byte(`{"model_type":"test"}`)
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "weights.bin"), weightsContent, 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "config.json"), configContent, 0o644))

		archivePath, err := CreateArchiveWithProgress(srcDir, "test-model", "v1", nil)
		require.NoError(t, err)
		defer os.Remove(archivePath)

		// Extract archive to a temp dir and verify checksums
		destDir := t.TempDir()
		f, err := os.Open(archivePath)
		require.NoError(t, err)
		defer f.Close()

		require.NoError(t, untarGzFromReader(f, destDir, nil))

		checksumDir := filepath.Join(destDir, ".neutree", "checksums")
		assert.DirExists(t, checksumDir)

		// Verify weights.bin checksum
		data, err := os.ReadFile(filepath.Join(checksumDir, "weights.bin.json"))
		require.NoError(t, err)
		var rec checksumRecord
		require.NoError(t, json.Unmarshal(data, &rec))
		assert.Equal(t, "sha256", rec.Algorithm)
		assert.Equal(t, sha256Hex(weightsContent), rec.Hash)

		// Verify config.json checksum
		data, err = os.ReadFile(filepath.Join(checksumDir, "config.json.json"))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &rec))
		assert.Equal(t, "sha256", rec.Algorithm)
		assert.Equal(t, sha256Hex(configContent), rec.Hash)

		// Verify model.yaml checksum exists (content is generated, just check it's present)
		data, err = os.ReadFile(filepath.Join(checksumDir, "model.yaml.json"))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &rec))
		assert.Equal(t, "sha256", rec.Algorithm)
		assert.NotEmpty(t, rec.Hash)
	})

	t.Run("archive handles subdirectories", func(t *testing.T) {
		srcDir := t.TempDir()

		subDir := filepath.Join(srcDir, "subdir")
		require.NoError(t, os.MkdirAll(subDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested"), 0o644))

		archivePath, err := CreateArchiveWithProgress(srcDir, "test-model", "v1", nil)
		require.NoError(t, err)
		defer os.Remove(archivePath)

		destDir := t.TempDir()
		f, err := os.Open(archivePath)
		require.NoError(t, err)
		defer f.Close()

		require.NoError(t, untarGzFromReader(f, destDir, nil))

		checksumPath := filepath.Join(destDir, ".neutree", "checksums", "subdir", "nested.txt.json")
		data, err := os.ReadFile(checksumPath)
		require.NoError(t, err)
		var rec checksumRecord
		require.NoError(t, json.Unmarshal(data, &rec))
		assert.Equal(t, sha256Hex([]byte("nested")), rec.Hash)
	})

	t.Run("model.yaml checksum matches actual content in archive", func(t *testing.T) {
		srcDir := t.TempDir()

		// Create a model.yaml that CreateArchiveWithProgress will modify
		require.NoError(t, os.WriteFile(filepath.Join(srcDir, "weights.bin"), []byte("data"), 0o644))

		archivePath, err := CreateArchiveWithProgress(srcDir, "test-model", "v1", nil)
		require.NoError(t, err)
		defer os.Remove(archivePath)

		destDir := t.TempDir()
		f, err := os.Open(archivePath)
		require.NoError(t, err)
		defer f.Close()

		require.NoError(t, untarGzFromReader(f, destDir, nil))

		// Read the actual model.yaml that was written to the archive
		actualYAML, err := os.ReadFile(filepath.Join(destDir, "model.yaml"))
		require.NoError(t, err)

		// Read the checksum record
		data, err := os.ReadFile(filepath.Join(destDir, ".neutree", "checksums", "model.yaml.json"))
		require.NoError(t, err)
		var rec checksumRecord
		require.NoError(t, json.Unmarshal(data, &rec))

		// Checksum should match the actual content in the archive
		assert.Equal(t, sha256Hex(actualYAML), rec.Hash)
	})
}

// "Not in the store" is the one read failure a caller answers differently, so
// it has to be told apart from the store being unreadable.
func TestGetModelDetailTellsAMissingModelFromAnUnreadableStore(t *testing.T) {
	home := t.TempDir()

	stored := filepath.Join(home, "models", "qwen3", "v1")
	require.NoError(t, os.MkdirAll(stored, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stored, ModelYAMLFileName),
		[]byte("name: qwen3\nversion: v1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, "models", "qwen3", v1.LatestVersion), []byte("v1\n"), 0o600))

	// A model directory that was created but never given a latest pointer.
	require.NoError(t, os.MkdirAll(filepath.Join(home, "models", "half-pushed", "v1"), 0o755))
	// A latest pointer that cannot be read as a file.
	require.NoError(t, os.MkdirAll(filepath.Join(home, "models", "broken", v1.LatestVersion), 0o755))

	cases := []struct {
		name         string
		model        string
		version      string
		wantNotFound bool
		wantErr      bool
	}{
		{name: "stored version", model: "qwen3", version: "v1"},
		{name: "stored model by latest", model: "qwen3", version: v1.LatestVersion},
		{name: "no such model", model: "nope", version: v1.LatestVersion, wantErr: true, wantNotFound: true},
		{name: "no such model, version named", model: "nope", version: "v1", wantErr: true, wantNotFound: true},
		{name: "directory without a latest pointer", model: "half-pushed", version: "", wantErr: true, wantNotFound: true},
		{name: "no such version", model: "qwen3", version: "v2", wantErr: true, wantNotFound: true},
		{name: "unreadable latest pointer", model: "broken", version: v1.LatestVersion, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model, err := GetModelDetail(home, tc.model, tc.version)

			if !tc.wantErr {
				require.NoError(t, err)
				require.Equal(t, "v1", model.Version)

				return
			}

			require.Error(t, err)
			require.Nil(t, model)
			require.Contains(t, err.Error(), tc.model)

			if tc.wantNotFound {
				require.ErrorIs(t, err, ErrModelNotFound)
			} else {
				require.NotErrorIs(t, err, ErrModelNotFound)
			}
		})
	}
}
