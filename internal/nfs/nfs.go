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

func MountNFS(device string, mountPoint string) error {
	err := os.MkdirAll(mountPoint, os.FileMode(0644))
	if err != nil {
		return err
	}

	mountPoints, err := mountInterface.List()
	if err != nil {
		return err
	}

	for _, mp := range mountPoints {
		if mountPoint == mp.Path && device == mp.Device {
			return nil
		}
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
