package nfs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

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
	mountChecksMu  sync.Mutex
	mountChecks    = make(map[string]*mountCheck)
	mountLocksMu   sync.Mutex
	mountLocks     = make(map[string]*mountLock)
)

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

func MountNFS(device string, mountPoint string) error {
	unlock := lockMountPoint(mountPoint)
	defer unlock()

	existed, unexpectedDevice, err := findMount(device, mountPoint)
	if err != nil {
		return err
	}

	if existed {
		return nil
	}

	if unexpectedDevice != "" {
		return errors.Errorf("mount point %s is already mounted from unexpected source %s", mountPoint, unexpectedDevice)
	}

	err = os.MkdirAll(mountPoint, 0o755)
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

type mountCheck struct {
	done chan struct{}
	err  error
}

type mountLock struct {
	mu    sync.Mutex
	users int
}

func lockMountPoint(mountPoint string) func() {
	mountLocksMu.Lock()
	lock := mountLocks[mountPoint]

	if lock == nil {
		lock = &mountLock{}
		mountLocks[mountPoint] = lock
	}

	lock.users++
	mountLocksMu.Unlock()

	lock.mu.Lock()

	return func() {
		lock.mu.Unlock()

		mountLocksMu.Lock()
		lock.users--

		if lock.users == 0 && mountLocks[mountPoint] == lock {
			delete(mountLocks, mountPoint)
		}
		mountLocksMu.Unlock()
	}
}

// WithMountLock serializes a mount-point operation with mount and unmount
// operations for the same target.
func WithMountLock(mountPoint string, operation func() error) error {
	unlock := lockMountPoint(mountPoint)
	defer unlock()

	return operation()
}

// CheckMountWithTimeout runs checkFunc with a caller timeout while coalescing
// concurrent checks for the same mount point.
func CheckMountWithTimeout(mountPoint string, timeout time.Duration, checkFunc func(string) error) error {
	mountChecksMu.Lock()
	check, loaded := mountChecks[mountPoint]

	if !loaded {
		check = &mountCheck{done: make(chan struct{})}
		mountChecks[mountPoint] = check
	}
	mountChecksMu.Unlock()

	if !loaded {
		go func() {
			check.err = checkFunc(mountPoint)
			close(check.done)

			forgetMountCheck(mountPoint, check)
		}()
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-check.done:
		if check.err != nil {
			return errors.Wrapf(check.err, "failed to check NFS mount path %s", mountPoint)
		}

		return nil
	case <-timer.C:
		return errors.Errorf("timed out checking NFS mount path %s", mountPoint)
	}
}

func forgetMountCheck(mountPoint string, check *mountCheck) {
	mountChecksMu.Lock()
	defer mountChecksMu.Unlock()

	if mountChecks[mountPoint] == check {
		delete(mountChecks, mountPoint)
	}
}

func resetMountCheck(mountPoint string) {
	mountChecksMu.Lock()
	defer mountChecksMu.Unlock()

	delete(mountChecks, mountPoint)
}

func Unmount(mountPoint string) error {
	unlock := lockMountPoint(mountPoint)
	defer unlock()

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

			resetMountCheck(mountPoint)

			break
		}
	}

	err = os.RemoveAll(mountPoint)
	if err != nil {
		return err
	}

	resetMountCheck(mountPoint)

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
