package utils

import (
	"os"

	kmount "k8s.io/utils/mount"
)

var (
	defaultNFSMountOptions = []string{
		"vers=4.1",
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

	err = mountInterface.Mount(device, mountPoint, "nfs", defaultNFSMountOptions)
	if err != nil {
		_ = os.RemoveAll(mountPoint)
	}

	return err
}

func Unmount(mountPoint string) error {
	mountPoints, err := mountInterface.List()
	if err != nil {
		return err
	}

	for _, mp := range mountPoints {
		if mountPoint == mp.Path {
			return mountInterface.Unmount(mountPoint)
		}
	}

	return nil
}
