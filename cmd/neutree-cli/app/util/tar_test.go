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

type testFile struct {
	name string
	data string
}

func createTestTar(t *testing.T, files []testFile) *bytes.Buffer {
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	for _, file := range files {
		hdr := &tar.Header{
			Name: file.name,
			Mode: 0644,
			Size: int64(len(file.data)),
		}

		if file.data == "" {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0755
		}

		err := tw.WriteHeader(hdr)
		assert.NoError(t, err)

		if file.data != "" {
			_, err = tw.Write([]byte(file.data))
			assert.NoError(t, err)
		}
	}

	err := tw.Close()
	assert.NoError(t, err)

	return buf
}

func TestExtractTar(t *testing.T) {
	tests := []struct {
		name        string
		files       []testFile // filename: content (empty string for dir)
		expectFiles []testFile // expected files and their content
		expectError bool
	}{
		{
			name: "extract single file",
			files: []testFile{
				{
					name: "test.txt",
					data: "hello world",
				},
			},
			expectFiles: []testFile{
				{
					name: "test.txt",
					data: "hello world",
				},
			},
			expectError: false,
		},
		{
			name: "extract directory structure",
			files: []testFile{
				{
					name: "dir/",
					data: "",
				},
				{
					name: "rootfile.txt",
					data: "root file",
				},
				{
					name: "dir/test.txt",
					data: "file in dir",
				},
			},
			expectFiles: []testFile{
				{
					name: "rootfile.txt",
					data: "root file",
				},
				{
					name: "dir/test.txt",
					data: "file in dir",
				},
			},
			expectError: false,
		},
		{
			name: "invalid tar data",
			files: []testFile{
				{
					name: "",
					data: "invalid tar data",
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			testDir := filepath.Join(tempDir, "test-extract")
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
			for _, expectFile := range tt.expectFiles {
				content, err := os.ReadFile(filepath.Join(testDir, expectFile.name))
				assert.NoError(t, err)
				assert.Equal(t, expectFile.data, string(content))
			}
		})
	}
}
