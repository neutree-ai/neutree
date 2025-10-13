package engine_version

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/pgzip"
	"github.com/pkg/errors"
)

// Extractor handles extraction of engine version packages
type Extractor struct{}

// NewExtractor creates a new Extractor
func NewExtractor() *Extractor {
	return &Extractor{}
}

// Extract extracts the engine version package to the specified directory
func (e *Extractor) Extract(packagePath, destPath string) error {
	// Create destination directory if it doesn't exist
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return errors.Wrap(err, "failed to create destination directory")
	}

	// Determine package format based on file extension
	ext := strings.ToLower(filepath.Ext(packagePath))
	switch ext {
	case ".gz", ".tgz":
		return e.extractTarGz(packagePath, destPath)
	default:
		return fmt.Errorf("unsupported package format: %s", ext)
	}
}

// extractTarGz extracts a .tar.gz package
func (e *Extractor) extractTarGz(packagePath, destPath string) error {
	file, err := os.Open(packagePath)
	if err != nil {
		return errors.Wrap(err, "failed to open package file")
	}
	defer file.Close()

	gzr, err := pgzip.NewReader(file)
	if err != nil {
		return errors.Wrap(err, "failed to create gzip reader")
	}
	defer gzr.Close()

	return e.extractTarReader(tar.NewReader(gzr), destPath)
}

// extractTarReader extracts files from a tar reader
func (e *Extractor) extractTarReader(tr *tar.Reader, destPath string) error {
	// Create a reusable buffer for better performance
	buf := make([]byte, 16*1024*1024) // 16MB buffer

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return errors.Wrap(err, "failed to read tar header")
		}

		// Construct the full path
		target := filepath.Join(destPath, header.Name) //nolint:gosec

		// Ensure the path is within destPath (security check)
		if !strings.HasPrefix(target, filepath.Clean(destPath)+string(os.PathSeparator)) {
			return fmt.Errorf("invalid file path: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return errors.Wrapf(err, "failed to create directory: %s", target)
			}
		case tar.TypeReg:
			if err := e.extractFile(tr, target, header.Mode, buf); err != nil {
				return errors.Wrapf(err, "failed to extract file: %s", target)
			}
		}
	}

	return nil
}

// extractFile extracts a single file
func (e *Extractor) extractFile(r io.Reader, target string, mode int64, buf []byte) error {
	// Create parent directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return errors.Wrap(err, "failed to create parent directory")
	}

	// Create the file
	file, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(mode))
	if err != nil {
		return errors.Wrap(err, "failed to create file")
	}
	defer file.Close()

	// Copy content using provided buffer for better performance
	if _, err := io.CopyBuffer(file, r, buf); err != nil {
		return errors.Wrap(err, "failed to write file content")
	}

	return nil
}
