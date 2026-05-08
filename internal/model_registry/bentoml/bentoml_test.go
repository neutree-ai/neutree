package bentoml

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
