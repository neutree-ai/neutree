package nfs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"

	"k8s.io/klog/v2"
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

var mountInterface = kmount.New("")

// IsMountExist checks whether the given NFS device is mounted at the specified
// mount point. It takes the device identifier (device) and the target mount
// path (mountPoint) as arguments, and returns true if a matching mount is
// found, false otherwise. An error is returned if the list of current mounts
// cannot be retrieved.
func IsMountExist(device string, mountPoint string) (bool, error) {
	existed, _, err := findMount(device, mountPoint)
	return existed, err
}

func findMount(device string, mountPoint string) (bool, string, error) {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return false, "", errors.Wrapf(err, "failed to list NFS mounts for %s", mountPoint)
	}

	var unexpectedDevice string

	for _, mp := range mountPoints {
		if mountPoint == mp.Path && device == mp.Device {
			return true, "", nil
		}

		if mountPoint == mp.Path && unexpectedDevice == "" {
			unexpectedDevice = mp.Device
		}
	}

	return false, unexpectedDevice, nil
}

// GetNFSVersion returns the NFS protocol version (e.g. "3", "4", "4.1", "4.2")
// for the given device mounted at mountPoint by reading /proc/mounts.
// It checks mount options first (vers=X / nfsvers=X) for the precise minor
// version, then falls back to the filesystem type ("nfs4" → "4").
// Defaults to "3" if no version information is found.
func GetNFSVersion(device string, mountPoint string) (string, error) {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return "", err
	}

	for _, mp := range mountPoints {
		if mp.Path == mountPoint && mp.Device == device {
			for _, opt := range mp.Opts {
				if strings.HasPrefix(opt, "vers=") {
					return strings.TrimPrefix(opt, "vers="), nil
				}

				if strings.HasPrefix(opt, "nfsvers=") {
					return strings.TrimPrefix(opt, "nfsvers="), nil
				}
			}

			if mp.Type == "nfs4" {
				return "4", nil
			}

			return "3", nil
		}
	}

	return "", errors.Errorf("mount %s at %s not found", device, mountPoint)
}

// MountNFS mounts device at mountPoint if it is not already mounted there. It
// takes no lease: use AcquireMount when the caller is about to read through the
// mount and must not have it pulled out from under it.
func MountNFS(device string, mountPoint string) error {
	guard := guardFor(mountPoint)

	guard.opMu.Lock()
	defer guard.opMu.Unlock()

	return mountLocked(device, mountPoint)
}

func mountLocked(device string, mountPoint string) error {
	existed, unexpectedDevice, err := findMount(device, mountPoint)
	if err != nil {
		return err
	}

	if existed {
		return nil
	}

	if unexpectedDevice != "" {
		return errors.Wrapf(ErrUnexpectedMountDevice,
			"mount point %s is already mounted from unexpected source %s", mountPoint, unexpectedDevice)
	}

	err = os.MkdirAll(mountPoint, 0o755)
	if err != nil {
		return err
	}

	err = mountInterface.Mount(device, mountPoint, "nfs", defaultNFSMountOptions)
	if err != nil {
		// Only the empty directory this call created is cleaned up. A recursive
		// delete here would erase the remote tree if another goroutine or process
		// mounted the same point in between.
		_ = os.Remove(mountPoint)

		return errors.Wrapf(err, "failed to mount nfs %s to %s", device, mountPoint)
	}

	return nil
}

// Unmount tears the mount point down. It refuses while any lease taken through
// AcquireMount is still held: a reader that is halfway through walking the tree
// must not have the mount removed under it.
func Unmount(mountPoint string) error {
	guard := guardFor(mountPoint)

	guard.opMu.Lock()
	defer guard.opMu.Unlock()

	if guard.held() > 0 {
		return errors.Wrapf(ErrMountBusy, "cannot unmount %s", mountPoint)
	}

	return unmountLocked(mountPoint)
}

func unmountLocked(mountPoint string) error {
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

	// os.Remove, not os.RemoveAll: the directory is only ours to delete once it is
	// empty. If the listing above missed a mount that is still live — a concurrent
	// remount, or a mount made in another process — a recursive delete would walk
	// into the remote tree and delete models. A non-empty leftover is local junk;
	// leaving it behind is harmless and the next mount reuses it.
	if err = os.Remove(mountPoint); err != nil && !os.IsNotExist(err) {
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EBUSY) {
			klog.Warningf("mount point %s is not empty after unmount, leaving it in place: %v", mountPoint, err)
			return nil
		}

		return err
	}

	return nil
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
