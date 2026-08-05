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

type blockingUnmountMounter struct {
	*kmount.FakeMounter
	unmounted chan struct{}
	release   chan struct{}
}

func (m *blockingUnmountMounter) Unmount(target string) error {
	if err := m.FakeMounter.Unmount(target); err != nil {
		return err
	}

	close(m.unmounted)
	<-m.release
	return nil
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
			name:       "existing expected mount with unreadable target is idempotent",
			targetKind: "file",
			mounter:    func() kmount.Interface { return kmount.NewFakeMounter(nil) },
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

func TestMountNFSCreatesTraversableTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	mounter := kmount.NewFakeMounter(nil)
	useMountInterface(t, mounter)

	require.NoError(t, MountNFS(testNFSDevice, target))

	info, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())
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

func TestCheckMountWithTimeout(t *testing.T) {
	release := make(chan struct{})
	completed := make(chan struct{})
	check := func(string) error {
		<-release
		close(completed)
		return nil
	}
	t.Cleanup(func() {
		close(release)
		<-completed
	})

	err := CheckMountWithTimeout("/mnt/registry", time.Millisecond, check)
	require.ErrorContains(t, err, "timed out checking NFS mount path /mnt/registry")
}

func TestCheckMountWithTimeoutRetriesAfterSuccessfulUnmount(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(target, 0o755))

	mounter := kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: testNFSDevice,
		Path:   target,
		Type:   "nfs",
	}})
	useMountInterface(t, mounter)

	release := make(chan struct{})
	completed := make(chan struct{})
	var calls atomic.Int32

	check := func(string) error {
		if calls.Add(1) == 1 {
			<-release
			close(completed)
		}

		return nil
	}
	t.Cleanup(func() {
		close(release)
		<-completed
	})

	err := CheckMountWithTimeout(target, time.Millisecond, check)
	require.ErrorContains(t, err, "timed out checking NFS mount path "+target)

	err = CheckMountWithTimeout(target, time.Millisecond, check)
	require.ErrorContains(t, err, "timed out checking NFS mount path "+target)
	require.EqualValues(t, 1, calls.Load())

	require.NoError(t, Unmount(target))
	require.NoError(t, CheckMountWithTimeout(target, time.Second, check))
	require.EqualValues(t, 2, calls.Load())
}

func TestCheckMountWithTimeoutRetriesWhenMountIsAlreadyAbsent(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(target, 0o755))
	useMountInterface(t, kmount.NewFakeMounter(nil))

	release := make(chan struct{})
	completed := make(chan struct{})
	var calls atomic.Int32
	check := func(string) error {
		if calls.Add(1) == 1 {
			<-release
			close(completed)
		}

		return nil
	}
	t.Cleanup(func() {
		close(release)
		<-completed
	})

	err := CheckMountWithTimeout(target, time.Millisecond, check)
	require.ErrorContains(t, err, "timed out checking NFS mount path "+target)

	require.NoError(t, Unmount(target))
	require.NoError(t, CheckMountWithTimeout(target, time.Second, check))
	require.EqualValues(t, 2, calls.Load())
}

func TestMountNFSWaitsForConcurrentUnmount(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(target, 0o755))

	mounter := &blockingUnmountMounter{
		FakeMounter: kmount.NewFakeMounter([]kmount.MountPoint{{
			Device: testNFSDevice,
			Path:   target,
			Type:   "nfs",
		}}),
		unmounted: make(chan struct{}),
		release:   make(chan struct{}),
	}
	useMountInterface(t, mounter)

	var releaseUnmountOnce sync.Once
	releaseUnmount := func() { releaseUnmountOnce.Do(func() { close(mounter.release) }) }

	unmountResult := make(chan error, 1)
	go func() { unmountResult <- Unmount(target) }()
	<-mounter.unmounted

	mountResult := make(chan error, 1)
	go func() { mountResult <- MountNFS(testNFSDevice, target) }()
	var unmountCollected bool
	var mountCollected bool

	t.Cleanup(func() {
		releaseUnmount()
		if !unmountCollected {
			require.NoError(t, <-unmountResult)
		}

		if !mountCollected {
			require.NoError(t, <-mountResult)
		}

	})

	require.Never(t, func() bool {
		for _, action := range mounter.GetLog() {
			if action.Action == kmount.FakeActionMount {
				return true
			}
		}

		return false
	}, 100*time.Millisecond, 5*time.Millisecond)

	releaseUnmount()
	require.NoError(t, <-unmountResult)
	unmountCollected = true
	require.NoError(t, <-mountResult)
	mountCollected = true
}

func TestWithMountLockWaitsForConcurrentUnmount(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(target, 0o755))
	mounter := &blockingUnmountMounter{
		FakeMounter: kmount.NewFakeMounter([]kmount.MountPoint{{
			Device: testNFSDevice,
			Path:   target,
			Type:   "nfs",
		}}),
		unmounted: make(chan struct{}),
		release:   make(chan struct{}),
	}
	useMountInterface(t, mounter)

	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	probeResult := make(chan error, 1)
	go func() {
		probeResult <- WithMountLock(target, func() error {
			close(probeStarted)
			<-releaseProbe
			return nil
		})
	}()
	<-probeStarted

	unmountResult := make(chan error, 1)
	go func() { unmountResult <- Unmount(target) }()
	require.Never(t, func() bool {
		select {
		case <-mounter.unmounted:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 10*time.Millisecond)

	close(releaseProbe)
	require.NoError(t, <-probeResult)
	<-mounter.unmounted
	close(mounter.release)
	require.NoError(t, <-unmountResult)
}

func TestCheckMountWithTimeoutCoalescesConcurrentChecks(t *testing.T) {
	target := filepath.Join(t.TempDir(), "registry")
	release := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	var calls atomic.Int32
	closeRelease := func() {
		releaseOnce.Do(func() { close(release) })
	}

	check := func(string) error {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}
	t.Cleanup(func() {
		closeRelease()
	})

	results := make(chan error, 2)
	go func() { results <- CheckMountWithTimeout(target, time.Second, check) }()
	<-started
	go func() { results <- CheckMountWithTimeout(target, time.Second, check) }()

	require.Never(t, func() bool {
		return calls.Load() > 1
	}, 100*time.Millisecond, 5*time.Millisecond)

	closeRelease()
	require.NoError(t, <-results)
	require.NoError(t, <-results)
}
