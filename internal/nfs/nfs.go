package nfs

import (
	"context"
	"fmt"
	"os"
	"strings"

	kmount "k8s.io/utils/mount"

	"github.com/pkg/errors"

	"github.com/neutree-ai/neutree/pkg/command_runner"
)

var (
	defaultNFSMountOptions = []string{
		"rsize=1048576",
		"wsize=1048576",
		"hard",
		"timeo=600",
		"retrans=2",
		"noresvport",
	}
)

var (
	mountInterface = kmount.New("")
)

// IsMountExist checks whether the given NFS device is mounted at the specified
// mount point. It takes the device identifier (device) and the target mount
// path (mountPoint) as arguments, and returns true if a matching mount is
// found, false otherwise. An error is returned if the list of current mounts
// cannot be retrieved.
func IsMountExist(device string, mountPoint string) (bool, error) {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return false, err
	}

	for _, mp := range mountPoints {
		if mountPoint == mp.Path && device == mp.Device {
			return true, nil
		}
	}

	return false, nil
}

// GetMountType returns the filesystem type (e.g. "nfs", "nfs4") for the
// given device mounted at mountPoint by reading /proc/mounts.
// Returns an error if the mount is not found.
func GetMountType(device string, mountPoint string) (string, error) {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return "", err
	}

	for _, mp := range mountPoints {
		if mp.Path == mountPoint && mp.Device == device {
			return mp.Type, nil
		}
	}

	return "", errors.Errorf("mount %s at %s not found", device, mountPoint)
}

// GetNFSVersion returns the NFS protocol version (e.g. "3", "4", "4.1", "4.2")
// for the given device mounted at mountPoint by reading /proc/mounts.
// It infers the version from the filesystem type ("nfs4" → "4") or from
// mount options ("vers=X" / "nfsvers=X"). Defaults to "3" if undetermined.
func GetNFSVersion(device string, mountPoint string) (string, error) {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return "", err
	}

	for _, mp := range mountPoints {
		if mp.Path == mountPoint && mp.Device == device {
			if mp.Type == "nfs4" {
				return "4", nil
			}

			for _, opt := range mp.Opts {
				if strings.HasPrefix(opt, "vers=") {
					return strings.TrimPrefix(opt, "vers="), nil
				}

				if strings.HasPrefix(opt, "nfsvers=") {
					return strings.TrimPrefix(opt, "nfsvers="), nil
				}
			}

			return "3", nil
		}
	}

	return "", errors.Errorf("mount %s at %s not found", device, mountPoint)
}

func MountNFS(device string, mountPoint string) error {
	existed, err := IsMountExist(device, mountPoint)
	if err != nil {
		return err
	}

	if existed {
		return nil
	}

	err = os.MkdirAll(mountPoint, os.FileMode(0644))
	if err != nil {
		return err
	}

	err = mountInterface.Mount(device, mountPoint, "nfs", defaultNFSMountOptions)
	if err != nil {
		_ = os.RemoveAll(mountPoint)
		return errors.Wrapf(err, "failed to mount nfs %s to %s", device, mountPoint)
	}

	return nil
}

func Unmount(mountPoint string) error {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return err
	}

	for _, mp := range mountPoints {
		if mountPoint == mp.Path {
			err = mountInterface.Unmount(mountPoint)
			if err != nil {
				return errors.Wrapf(err, "failed to unmount nfs from %s", mountPoint)
			}

			break
		}
	}

	return os.RemoveAll(mountPoint)
}

type KubernetesNfsMounter struct {
	commandRunner command_runner.KubernetesCommandRunner
}

func NewKubernetesNfsMounter(commandRunner command_runner.KubernetesCommandRunner) *KubernetesNfsMounter {
	return &KubernetesNfsMounter{
		commandRunner: commandRunner,
	}
}

func (m *KubernetesNfsMounter) MountNFS(ctx context.Context, device string, mountPoint string) error {
	output, err := m.commandRunner.Run(ctx, fmt.Sprintf(`sudo mount  -l; echo "EXIT_CODE:$?"`))
	if err != nil {
		return err
	}

	if strings.Contains(output, "EXIT_CODE:0") && strings.Contains(output, mountPoint) {
		return nil
	}

	_, err = m.commandRunner.Run(ctx, fmt.Sprintf("sudo mkdir -p %s", mountPoint))
	if err != nil {
		return err
	}

	output, err = m.commandRunner.Run(ctx, fmt.Sprintf(`sudo mount -t nfs -o %s %s %s; echo "EXIT_CODE:$?"`, strings.Join(defaultNFSMountOptions, ","), device, mountPoint))
	if err != nil {
		return err
	}

	if !strings.Contains(output, "EXIT_CODE:0") {
		return fmt.Errorf("mount nfs failed")
	}

	return nil
}

func (m *KubernetesNfsMounter) Unmount(ctx context.Context, mountPoint string) error {
	output, err := m.commandRunner.Run(ctx, fmt.Sprintf(`sudo mount  -l; echo "EXIT_CODE:$?"`))
	if err != nil {
		return err
	}

	if strings.Contains(output, "EXIT_CODE:0") && !strings.Contains(output, mountPoint) {
		return nil
	}

	_, err = m.commandRunner.Run(ctx, fmt.Sprintf(`sudo umount %s; echo "EXIT_CODE:$?"`, mountPoint))
	if err != nil {
		return err
	}

	if strings.Contains(output, "EXIT_CODE:0") {
		return nil
	}

	return fmt.Errorf("unmount nfs failed")
}

type DockerNfsMounter struct {
	commandRunner command_runner.DockerCommandRunner
}

func NewDockerNfsMounter(commandRunner command_runner.DockerCommandRunner) *DockerNfsMounter {
	return &DockerNfsMounter{
		commandRunner: commandRunner,
	}
}

func (m *DockerNfsMounter) MountNFS(ctx context.Context, device string, mountPoint string) error {
	output, err := m.commandRunner.Run(ctx, fmt.Sprintf(`sudo mount  -l; echo "EXIT_CODE:$?"`), true, nil, true, nil, "docker", "", false)
	if err != nil {
		return err
	}

	if strings.Contains(output, "EXIT_CODE:0") && strings.Contains(output, mountPoint) {
		return nil
	}

	_, err = m.commandRunner.Run(ctx, fmt.Sprintf("sudo mkdir -p %s", mountPoint), true, nil, true, nil, "docker", "", false)
	if err != nil {
		return err
	}

	output, err = m.commandRunner.Run(ctx, fmt.Sprintf(`sudo mount -t nfs -o %s %s %s; echo "EXIT_CODE:$?"`,
		strings.Join(defaultNFSMountOptions, ","), device, mountPoint), true, nil, true, nil, "docker", "", false)
	if err != nil {
		return err
	}

	if !strings.Contains(output, "EXIT_CODE:0") {
		return fmt.Errorf("mount nfs failed")
	}

	return nil
}

func (m *DockerNfsMounter) Unmount(ctx context.Context, mountPoint string) error {
	output, err := m.commandRunner.Run(ctx, fmt.Sprintf(`sudo mount  -l; echo "EXIT_CODE:$?"`), true, nil, true, nil, "docker", "", false)
	if err != nil {
		return err
	}

	if strings.Contains(output, "EXIT_CODE:0") && !strings.Contains(output, mountPoint) {
		return nil
	}

	_, err = m.commandRunner.Run(ctx, fmt.Sprintf(`sudo umount %s; echo "EXIT_CODE:$?"`, mountPoint), true, nil, true, nil, "docker", "", false)
	if err != nil {
		return err
	}

	if strings.Contains(output, "EXIT_CODE:0") {
		return nil
	}

	return fmt.Errorf("unmount nfs failed")
}
