package command_runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/pkg/errors"
)

// FileClient manages files on the SSH host.
type FileClient interface {
	WriteFileIfChanged(ctx context.Context, remotePath string, content []byte, opts WriteFileOptions) (bool, error)
	Remove(ctx context.Context, remotePath string, opts RemoveFileOptions) error
}

type WriteFileOptions struct {
	Mode         string
	Owner        string
	Group        string
	Sudo         bool
	Atomic       bool
	CreateParent bool
}

type RemoveFileOptions struct {
	Sudo bool
}

type sshFileClient struct {
	runner *SSHCommandRunner
}

// Files returns a host-level file client backed by the SSH connection.
func (s *SSHCommandRunner) Files() FileClient {
	return &sshFileClient{runner: s}
}

func (f *sshFileClient) WriteFileIfChanged(
	ctx context.Context,
	remotePath string,
	content []byte,
	opts WriteFileOptions,
) (bool, error) {
	localHash := sha256Hex(content)

	remoteHash, err := f.remoteSha256(ctx, remotePath, opts.Sudo)
	if err != nil {
		return false, err
	}

	if remoteHash == localHash {
		return false, nil
	}

	if err := f.writeFile(ctx, remotePath, content, opts); err != nil {
		return false, err
	}

	return true, nil
}

func (f *sshFileClient) writeFile(
	ctx context.Context,
	remotePath string,
	content []byte,
	opts WriteFileOptions,
) error {
	localPath, err := writeTempFile(content)
	if err != nil {
		return err
	}
	defer os.Remove(localPath) //nolint:errcheck

	hash := sha256Hex(content)
	remotePathHash := sha256Hex([]byte(remotePath))

	remoteTmpPath := "/tmp/neutree-remote-file-" + hash[:16] + "-" + remotePathHash[:16] + ".tmp"
	if err := f.upload(ctx, localPath, remoteTmpPath); err != nil {
		return err
	}

	if opts.CreateParent {
		mkdirCmd := withSudo(opts.Sudo, "mkdir -p "+shellQuote(path.Dir(remotePath)))
		if _, err := f.runner.Run(ctx, mkdirCmd, true, nil, false, nil, "", false); err != nil {
			return errors.Wrap(err, "failed to create remote parent directory")
		}
	}

	installCmd := f.installCommand(remoteTmpPath, remotePath, hash[:16], opts)
	if _, err := f.runner.Run(ctx, installCmd, true, nil, false, nil, "", false); err != nil {
		return errors.Wrap(err, "failed to install remote file")
	}

	return nil
}

func (f *sshFileClient) Remove(ctx context.Context, remotePath string, opts RemoveFileOptions) error {
	_, err := f.runner.Run(ctx, withSudo(opts.Sudo, "rm -f "+shellQuote(remotePath)), true, nil, false, nil, "", false)
	if err != nil {
		return errors.Wrap(err, "failed to remove remote file")
	}

	return nil
}

func (f *sshFileClient) remoteSha256(ctx context.Context, remotePath string, sudo bool) (string, error) {
	command := "if command -v sha256sum >/dev/null 2>&1 && test -f " + shellQuote(remotePath) +
		"; then sha256sum " + shellQuote(remotePath) + " | awk '{print $1}'; fi"

	output, err := f.runner.Run(ctx, withSudo(sudo, command), true, nil, true, nil, "", false)
	if err != nil {
		return "", errors.Wrap(err, "failed to calculate remote file hash")
	}

	return strings.TrimSpace(output), nil
}

func (f *sshFileClient) upload(ctx context.Context, localPath, remotePath string) error {
	scpArgs := append([]string{}, f.runner.getSSHOptions("")...)
	scpArgs = append(scpArgs, localPath, fmt.Sprintf("%s@%s:%s", f.runner.sshUser, f.runner.sshIP, remotePath))

	_, err := f.runner.processExecute(ctx, "scp", scpArgs)
	if err != nil {
		return errors.Wrap(err, "failed to upload remote file")
	}

	return nil
}

func (f *sshFileClient) installCommand(remoteTmpPath, remotePath, hashPrefix string, opts WriteFileOptions) string {
	mode := opts.Mode
	if mode == "" {
		mode = "0644"
	}

	installParts := []string{"install", "-m", mode}
	if opts.Owner != "" {
		installParts = append(installParts, "-o", shellQuote(opts.Owner))
	}

	if opts.Group != "" {
		installParts = append(installParts, "-g", shellQuote(opts.Group))
	}

	if !opts.Atomic {
		installParts = append(installParts, shellQuote(remoteTmpPath), shellQuote(remotePath))

		return strings.Join([]string{
			withSudo(opts.Sudo, strings.Join(installParts, " ")),
			withSudo(opts.Sudo, "rm -f "+shellQuote(remoteTmpPath)),
		}, " && ")
	}

	stagedPath := remotePath + ".neutree-tmp-" + hashPrefix
	installParts = append(installParts, shellQuote(remoteTmpPath), shellQuote(stagedPath))

	return strings.Join([]string{
		withSudo(opts.Sudo, strings.Join(installParts, " ")),
		withSudo(opts.Sudo, "mv "+shellQuote(stagedPath)+" "+shellQuote(remotePath)),
		withSudo(opts.Sudo, "rm -f "+shellQuote(remoteTmpPath)),
	}, " && ")
}

func writeTempFile(content []byte) (string, error) {
	file, err := os.CreateTemp("", "neutree-remote-file-*")
	if err != nil {
		return "", errors.Wrap(err, "failed to create local temp file")
	}

	if _, err := file.Write(content); err != nil {
		file.Close()           //nolint:errcheck
		os.Remove(file.Name()) //nolint:errcheck

		return "", errors.Wrap(err, "failed to write local temp file")
	}

	if err := file.Close(); err != nil {
		os.Remove(file.Name()) //nolint:errcheck
		return "", errors.Wrap(err, "failed to close local temp file")
	}

	return file.Name(), nil
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func withSudo(sudo bool, command string) string {
	if sudo {
		return "sudo sh -c " + shellQuote(command)
	}

	return command
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
