package util

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func createTestTar(t *testing.T, files map[string]string) *bytes.Buffer {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}

		if content == "" {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0755
		}

		err := tw.WriteHeader(hdr)
		assert.NoError(t, err)

		if content != "" {
			_, err = tw.Write([]byte(content))
			assert.NoError(t, err)
		}
	}

	err := tw.Close()
	assert.NoError(t, err)

	return buf
}

func TestExtractTar(t *testing.T) {
	tempDir := os.TempDir()
	testDir := filepath.Join(tempDir, "test-extract")
	defer os.RemoveAll(testDir)

	tests := []struct {
		name        string
		files       map[string]string // filename: content (empty string for dir)
		expectFiles map[string]string // expected files and their content
		expectError bool
	}{
		{
			name: "extract single file",
			files: map[string]string{
				"test.txt": "hello world",
			},
			expectFiles: map[string]string{
				"test.txt": "hello world",
			},
			expectError: false,
		},
		{
			name: "extract directory structure",
			files: map[string]string{
				"dir/":         "",
				"dir/test.txt": "file in dir",
				"rootfile.txt": "root file",
			},
			expectFiles: map[string]string{
				"dir/test.txt": "file in dir",
				"rootfile.txt": "root file",
			},
			expectError: false,
		},
		{
			name: "invalid tar data",
			files: map[string]string{
				"": "invalid data",
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test tar
			tarData := createTestTar(t, tt.files)
			tarFile := bytes.NewReader(tarData.Bytes())

			// Create fs.File from bytes.Reader
			file := struct {
				io.Reader
				io.Seeker
				io.Closer
			}{
				Reader: tarFile,
				Seeker: tarFile,
				Closer: io.NopCloser(nil),
			}

			// Create test directory
			err := os.MkdirAll(testDir, 0755)
			assert.NoError(t, err)

			// Run function
			err = ExtractTar(file, testDir)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			// Verify extracted files
			for name, expectedContent := range tt.expectFiles {
				content, err := os.ReadFile(filepath.Join(testDir, name))
				assert.NoError(t, err)
				assert.Equal(t, expectedContent, string(content))
			}

			// Cleanup
			os.RemoveAll(testDir)
		})
	}
}
