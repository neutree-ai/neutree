package nfs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	kmount "k8s.io/utils/mount"
)

const testNFSDevice = "nfs.example.internal:/exports/models"

type failingMounter struct {
	kmount.Interface
	err error
}

func (m *failingMounter) Mount(source, target, fstype string, options []string) error {
	return m.err
}

type listFailingMounter struct {
	kmount.Interface
	err error
}

func (m *listFailingMounter) List() ([]kmount.MountPoint, error) {
	return nil, m.err
}

func useMountInterface(t *testing.T, mounter kmount.Interface) {
	t.Helper()

	original := mountInterface
	mountInterface = mounter
	t.Cleanup(func() {
		mountInterface = original
	})
}

func TestMountNFS(t *testing.T) {
	tests := []struct {
		name       string
		targetKind string
		mounter    func() kmount.Interface
		wantErr    string
		wantMount  bool
	}{
		{
			name:       "existing expected mount with readable directory is idempotent",
			targetKind: "directory",
			mounter:    func() kmount.Interface { return kmount.NewFakeMounter(nil) },
		},
		{
			name:       "missing expected mount is created",
			targetKind: "directory",
			mounter:    func() kmount.Interface { return kmount.NewFakeMounter(nil) },
			wantMount:  true,
		},
		{
			name:       "mount failure is returned",
			targetKind: "missing",
			mounter: func() kmount.Interface {
				return &failingMounter{
					Interface: kmount.NewFakeMounter(nil),
					err:       errors.New("mount failed"),
				}
			},
			wantErr:   "failed to mount nfs",
			wantMount: true,
		},
		{
			name:       "existing expected mount with unreadable root fails with target path",
			targetKind: "file",
			mounter:    func() kmount.Interface { return kmount.NewFakeMounter(nil) },
			wantErr:    "registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseDir := t.TempDir()
			target := filepath.Join(baseDir, "registry")

			switch tt.targetKind {
			case "directory":
				require.NoError(t, os.Mkdir(target, 0o755))
			case "file":
				require.NoError(t, os.WriteFile(target, nil, 0o644))
			}

			mounter := tt.mounter()
			if tt.targetKind != "missing" {
				fakeMounter, ok := mounter.(*kmount.FakeMounter)
				if ok && tt.name != "missing expected mount is created" {
					fakeMounter.MountPoints = []kmount.MountPoint{{
						Device: testNFSDevice,
						Path:   target,
						Type:   "nfs",
					}}
				}
			}

			useMountInterface(t, mounter)
			err := MountNFS(testNFSDevice, target)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}

			if fakeMounter, ok := mounter.(*kmount.FakeMounter); ok {
				mountActions := fakeMounter.GetLog()
				if tt.wantMount {
					expectedTarget, err := filepath.EvalSymlinks(target)
					if err != nil {
						expectedTarget = target
					}

					require.Len(t, mountActions, 1)
					require.Equal(t, testNFSDevice, mountActions[0].Source)
					require.Equal(t, expectedTarget, mountActions[0].Target)
				} else {
					require.Empty(t, mountActions)
				}
			}
		})
	}
}

func TestIsMountExistRequiresMatchingSourceAndTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	useMountInterface(t, kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: "other-nfs.example.internal:/exports/models",
		Path:   target,
		Type:   "nfs",
	}}))

	exists, err := IsMountExist(testNFSDevice, target)
	require.NoError(t, err)
	require.False(t, exists)
}

func TestMountNFSRejectsTargetMountedFromUnexpectedSource(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(target, 0o755))

	const unexpectedDevice = "other-nfs.example.internal:/exports/models"
	mounter := kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: unexpectedDevice,
		Path:   target,
		Type:   "nfs",
	}})
	useMountInterface(t, mounter)

	err := MountNFS(testNFSDevice, target)
	require.ErrorContains(t, err, target)
	require.ErrorContains(t, err, unexpectedDevice)
	require.Empty(t, mounter.GetLog())
}

func TestMountNFSIncludesTargetWhenMountListingFails(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	useMountInterface(t, &listFailingMounter{
		Interface: kmount.NewFakeMounter(nil),
		err:       errors.New("mount list failed"),
	})

	err := MountNFS(testNFSDevice, target)
	require.ErrorContains(t, err, target)
	require.ErrorContains(t, err, "mount list failed")
}

func TestReadDirWithTimeout(t *testing.T) {
	release := make(chan struct{})
	completed := make(chan struct{})
	originalReadDir := readDir
	readDir = func(string) ([]os.DirEntry, error) {
		<-release
		close(completed)
		return nil, nil
	}
	t.Cleanup(func() {
		close(release)
		<-completed
		readDir = originalReadDir
	})

	err := readDirWithTimeout("/mnt/registry", time.Millisecond)
	require.ErrorContains(t, err, "timed out reading NFS mount path /mnt/registry")
}

func TestReadDirWithTimeoutAllowsRetryAfterTimeout(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	release := make(chan struct{})
	completed := make(chan struct{})
	var calls atomic.Int32

	originalReadDir := readDir
	readDir = func(string) ([]os.DirEntry, error) {
		if calls.Add(1) == 1 {
			<-release
			close(completed)
		}

		return nil, nil
	}
	t.Cleanup(func() {
		close(release)
		<-completed
		readDir = originalReadDir
	})

	err := readDirWithTimeout(target, time.Millisecond)
	require.ErrorContains(t, err, "timed out reading NFS mount path "+target)

	require.NoError(t, readDirWithTimeout(target, time.Second))
	require.EqualValues(t, 2, calls.Load())
}

func TestReadDirWithTimeoutCoalescesConcurrentChecks(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	release := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	var calls atomic.Int32
	closeRelease := func() {
		releaseOnce.Do(func() { close(release) })
	}

	originalReadDir := readDir
	readDir = func(string) ([]os.DirEntry, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		return nil, nil
	}
	t.Cleanup(func() {
		readDir = originalReadDir
		closeRelease()
	})

	results := make(chan error, 2)
	go func() { results <- readDirWithTimeout(target, time.Second) }()
	<-started
	go func() { results <- readDirWithTimeout(target, time.Second) }()

	require.Never(t, func() bool {
		return calls.Load() > 1
	}, 100*time.Millisecond, 5*time.Millisecond)

	closeRelease()
	require.NoError(t, <-results)
	require.NoError(t, <-results)
}
