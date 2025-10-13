package util

import (
	"encoding/base64"
	"os"
	"path"

	"github.com/pkg/errors"
)

func GenerateTmpSSHKeyFile(sshPrivateKey string) (string, error) {
	decodedKey, err := base64.StdEncoding.DecodeString(sshPrivateKey)
	if err != nil {
		return "", errors.Wrap(err, "decode ssh key failed")
	}

	tmpDir, err := os.MkdirTemp("", "ssh-key-")
	if err != nil {
		return "", errors.Wrap(err, "create tmp dir failed")
	}

	sshKeyPath := path.Join(tmpDir, "ssh_key")
	if err = os.WriteFile(sshKeyPath, decodedKey, 0600); err != nil {
		return "", errors.Wrap(err, "write ssh key failed")
	}

	return sshKeyPath, nil
}
