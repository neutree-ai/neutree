package launch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

var kongPluginNames = []string{
	"neutree-ai-gateway",
	"neutree-ai-statistics",
	"neutree-ai-access",
	"neutree-ai-quota",
}

func kongPluginChecksums(pluginsRoot string) (map[string]string, error) {
	checksums := make(map[string]string, len(kongPluginNames))
	for _, pluginName := range kongPluginNames {
		checksum, err := kongPluginChecksum(filepath.Join(pluginsRoot, pluginName))
		if err != nil {
			return nil, fmt.Errorf("checksum Kong plugin %s: %w", pluginName, err)
		}

		checksums[pluginName] = checksum
	}

	return checksums, nil
}

func kongPluginChecksum(pluginDir string) (string, error) {
	info, err := os.Lstat(pluginDir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("expected directory %s", pluginDir)
	}

	hasher := sha256.New()

	err = filepath.WalkDir(pluginDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("non-regular file %s", path)
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file %s", path)
		}

		relativePath, err := filepath.Rel(pluginDir, path)
		if err != nil {
			return err
		}
		if err := writeKongPluginChecksumRecord(hasher, filepath.ToSlash(relativePath), path); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeKongPluginChecksumRecord(hasher hash.Hash, relativePath, path string) error {
	if _, err := io.WriteString(hasher, relativePath); err != nil {
		return err
	}
	if _, err := hasher.Write([]byte{0}); err != nil {
		return err
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	if _, err := hasher.Write([]byte{0}); err != nil {
		return err
	}

	return nil
}
