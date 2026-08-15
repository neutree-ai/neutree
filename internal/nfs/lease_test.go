package nfs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	kmount "k8s.io/utils/mount"
)

// mountedFakeMounter is a fake whose mount table already contains target, the
// state every one of these tests starts from: the registry is mounted and being
// read.
func mountedFakeMounter(target string) *kmount.FakeMounter {
	return kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: testNFSDevice,
		Path:   target,
		Type:   "nfs",
	}})
}

// targetDir returns a mount point path under a temporary directory, with symlinks
// already resolved: the fake mounter records what the kernel would report, and on
// macOS the temporary directory is reached through a symlink.
func targetDir(t *testing.T) string {
	t.Helper()

	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return filepath.Join(base, "registry")
}

func mountedTarget(t *testing.T) string {
	t.Helper()

	target := targetDir(t)
	require.NoError(t, os.Mkdir(target, 0o755))

	return target
}

// The bug this whole mechanism exists for: one reader finishing pulled the mount
// out from under the readers still walking it.
func TestUnmountIsRefusedWhileALeaseIsHeld(t *testing.T) {
	target := mountedTarget(t)
	mounter := mountedFakeMounter(target)
	useMountInterface(t, mounter)

	require.NoError(t, AcquireMount(testNFSDevice, target))

	err := Unmount(target)
	require.ErrorIs(t, err, ErrMountBusy)
	require.ErrorContains(t, err, target)

	exists, err := IsMountExist(testNFSDevice, target)
	require.NoError(t, err)
	require.True(t, exists, "the mount a reader still holds must survive another client's teardown")
}

// Releasing is not tearing down. A read finishing leaves the registry mounted for
// the next one — unmount is a lifecycle decision, made by the paths that own the
// registry.
func TestReleaseMountLeavesTheMountInPlace(t *testing.T) {
	target := mountedTarget(t)
	useMountInterface(t, mountedFakeMounter(target))

	require.NoError(t, AcquireMount(testNFSDevice, target))
	ReleaseMount(target)

	exists, err := IsMountExist(testNFSDevice, target)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestUnmountSucceedsOnceEveryLeaseIsReleased(t *testing.T) {
	target := mountedTarget(t)
	useMountInterface(t, mountedFakeMounter(target))

	require.NoError(t, AcquireMount(testNFSDevice, target))
	require.NoError(t, AcquireMount(testNFSDevice, target))

	ReleaseMount(target)
	require.ErrorIs(t, Unmount(target), ErrMountBusy, "one reader is still holding the mount")

	ReleaseMount(target)
	require.NoError(t, Unmount(target))

	exists, err := IsMountExist(testNFSDevice, target)
	require.NoError(t, err)
	require.False(t, exists)
	require.NoFileExists(t, target)
}

// A mount point that still holds files after the unmount is left alone. Deleting
// it recursively is how a teardown racing a remount erases the remote model tree.
func TestUnmountDoesNotDeleteDirectoryContents(t *testing.T) {
	target := mountedTarget(t)
	useMountInterface(t, mountedFakeMounter(target))

	model := filepath.Join(target, "model.yaml")
	require.NoError(t, os.WriteFile(model, []byte("name: qwen3\n"), 0o644))

	require.NoError(t, Unmount(target))
	require.FileExists(t, model, "unmount must not remove what is inside the mount point")
}

// Concurrent readers of one registry: they share the mount, they mount it once,
// and none of them ends up without it.
func TestConcurrentLeasesShareASingleMount(t *testing.T) {
	target := targetDir(t)
	mounter := kmount.NewFakeMounter(nil)
	useMountInterface(t, mounter)

	const readers = 16

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for range readers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			if err := AcquireMount(testNFSDevice, target); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()

				return
			}
			defer ReleaseMount(target)

			// Every reader must find the mount present for as long as it holds a
			// lease, whatever the others are doing.
			exists, err := IsMountExist(testNFSDevice, target)
			if err == nil && !exists {
				err = errors.New("mount disappeared while a lease was held")
			}

			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	require.Empty(t, errs)
	require.Len(t, mounter.GetLog(), 1, "the shared mount point is mounted once, not once per reader")

	// With every lease given back, the owner can still tear it down.
	require.NoError(t, Unmount(target))
}

// A registry whose URL was edited leaves the old export mounted at a mount point
// derived from the registry's identity. Since reads no longer unmount on their
// way out, nothing would ever clear it — so acquiring replaces it, but only while
// no one is reading through it.
func TestAcquireMountReplacesAStaleMountOfAnotherExport(t *testing.T) {
	const staleDevice = "old-nfs.example.internal:/exports/models"

	target := mountedTarget(t)
	mounter := kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: staleDevice,
		Path:   target,
		Type:   "nfs",
	}})
	useMountInterface(t, mounter)

	require.NoError(t, AcquireMount(testNFSDevice, target))
	defer ReleaseMount(target)

	exists, err := IsMountExist(testNFSDevice, target)
	require.NoError(t, err)
	require.True(t, exists)
}

func TestAcquireMountKeepsAStaleMountThatIsBeingRead(t *testing.T) {
	const staleDevice = "old-nfs.example.internal:/exports/models"

	target := mountedTarget(t)
	useMountInterface(t, kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: staleDevice,
		Path:   target,
		Type:   "nfs",
	}}))

	require.NoError(t, AcquireMount(staleDevice, target))
	defer ReleaseMount(target)

	err := AcquireMount(testNFSDevice, target)
	require.ErrorIs(t, err, ErrUnexpectedMountDevice)

	exists, err := IsMountExist(staleDevice, target)
	require.NoError(t, err)
	require.True(t, exists, "a mount someone is reading is never replaced under them")
}
