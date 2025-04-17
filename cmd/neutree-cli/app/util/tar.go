package util

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func ExtractTar(fileReader io.Reader, dst string) error {
	tr := tar.NewReader(fileReader)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		targetPath := filepath.Join(dst, header.Name) //nolint:gosec

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil { //nolint:gosec
				return err
			}
		case tar.TypeReg:
			file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode)) //nolint:gosec
			if err != nil {
				return err
			}
			defer file.Close()

			if _, err := io.Copy(file, tr); err != nil { //nolint:gosec
				return err
			}
		default:
			return fmt.Errorf("unsupported tar entry type: %v", header.Typeflag)
		}
	}

	return nil
}
