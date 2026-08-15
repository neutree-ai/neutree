package nfs

import (
	kmount "k8s.io/utils/mount"
)

// SetMountInterfaceForTesting swaps the mounter this package drives and returns
// a function that puts the original back. It exists so packages that read
// through an NFS mount — the model registry, above all — can be tested against a
// mount table they control instead of the host's.
func SetMountInterfaceForTesting(mounter kmount.Interface) func() {
	original := mountInterface
	mountInterface = mounter

	return func() {
		mountInterface = original
	}
}
