package model_registry

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	kmount "k8s.io/utils/mount"

	"github.com/neutree-ai/neutree/internal/nfs"
)

const testNFSServerPath = "nfs.example.internal:/exports/models"

// newTestNFSRegistry builds an NFS-backed registry over a real directory, with a
// mount table that says the directory is that registry's mount. Symlinks are
// resolved because the mount table records what the kernel would report.
func newTestNFSRegistry(t *testing.T) (*nfsFile, *kmount.FakeMounter) {
	t.Helper()

	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	mountPoint := filepath.Join(base, "registry")
	require.NoError(t, os.Mkdir(mountPoint, 0o755))

	mounter := kmount.NewFakeMounter([]kmount.MountPoint{{
		Device: testNFSServerPath,
		Path:   mountPoint,
		Type:   "nfs",
	}})
	t.Cleanup(nfs.SetMountInterfaceForTesting(mounter))

	return &nfsFile{
		bentomlStore:  bentomlStore{path: mountPoint},
		nfsServerPath: testNFSServerPath,
	}, mounter
}

// A registry with nothing in it and a registry whose mount vanished read exactly
// the same from the filesystem. Reporting the second as an empty list is how a
// storage outage reached users as "this registry has no models".
func TestNFSListModelsReportsAVanishedMountInsteadOfAnEmptyList(t *testing.T) {
	registry, mounter := newTestNFSRegistry(t)

	mounter.MountPoints = nil

	page, err := registry.ListModels(ListOption{})
	require.Nil(t, page)
	require.ErrorIs(t, err, ErrStorageUnavailable)
	require.ErrorContains(t, err, registry.path)
}

func TestNFSListModelsAcceptsAnEmptyMountedRegistry(t *testing.T) {
	registry, _ := newTestNFSRegistry(t)

	page, err := registry.ListModels(ListOption{})
	require.NoError(t, err)
	require.Empty(t, page.Models)
	require.NotNil(t, page.Total)
	require.Equal(t, 0, *page.Total)
}

func TestNFSListModelsReturnsModelsWhileMounted(t *testing.T) {
	registry, _ := newTestNFSRegistry(t)
	writeStoredModel(t, registry.path, "qwen3", "v1", nil)

	page, err := registry.ListModels(ListOption{})
	require.NoError(t, err)
	require.Len(t, page.Models, 1)
	require.Equal(t, "qwen3", page.Models[0].Name)
}

// "Model not found" is a statement about the registry's contents. When the mount
// is gone we do not know the contents, and saying it anyway sends the user
// looking for a model they never lost.
func TestNFSModelReadsReportAVanishedMountInsteadOfNotFound(t *testing.T) {
	registry, mounter := newTestNFSRegistry(t)
	writeStoredModel(t, registry.path, "qwen3", "v1", nil)

	mounter.MountPoints = nil
	require.NoError(t, os.RemoveAll(filepath.Join(registry.path, "models")))

	t.Run("detail", func(t *testing.T) {
		_, err := registry.GetModelDetail("qwen3", "v1")
		require.ErrorIs(t, err, ErrStorageUnavailable)
	})

	t.Run("version", func(t *testing.T) {
		_, err := registry.GetModelVersion("qwen3", "v1")
		require.ErrorIs(t, err, ErrStorageUnavailable)
	})

	t.Run("readme", func(t *testing.T) {
		_, err := registry.GetReadme("qwen3", "v1")
		require.ErrorIs(t, err, ErrStorageUnavailable)
	})
}

// A genuine miss on a healthy mount stays a miss.
func TestNFSMissingModelOnAHealthyMountStaysNotFound(t *testing.T) {
	registry, _ := newTestNFSRegistry(t)
	writeStoredModel(t, registry.path, "qwen3", "v1", nil)

	_, err := registry.GetModelVersion("qwen3", "v2")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrStorageUnavailable)
}

// The reported failure: three concurrent readers of one registry, and whichever
// finished first unmounted the tree the other two were walking.
func TestNFSDisconnectingOneReaderKeepsTheMountForTheOthers(t *testing.T) {
	first, _ := newTestNFSRegistry(t)
	writeStoredModel(t, first.path, "qwen3", "v1", nil)

	second := &nfsFile{bentomlStore: bentomlStore{path: first.path}, nfsServerPath: testNFSServerPath}
	third := &nfsFile{bentomlStore: bentomlStore{path: first.path}, nfsServerPath: testNFSServerPath}

	for _, reader := range []*nfsFile{first, second, third} {
		require.NoError(t, reader.Connect())
	}

	require.NoError(t, first.Disconnect())

	for _, reader := range []*nfsFile{second, third} {
		page, err := reader.ListModels(ListOption{})
		require.NoError(t, err)
		require.Len(t, page.Models, 1)
		require.NoError(t, reader.HealthyCheck())
	}

	require.NoError(t, second.Disconnect())
	require.NoError(t, third.Disconnect())
}

// A client that never connected is a lifecycle caller — deletion, or a reconnect
// after failure. It is the one that unmounts, and it is refused while readers are
// still holding the mount.
func TestNFSTeardownWaitsForActiveReaders(t *testing.T) {
	reader, _ := newTestNFSRegistry(t)

	owner := &nfsFile{bentomlStore: bentomlStore{path: reader.path}, nfsServerPath: testNFSServerPath}

	require.NoError(t, reader.Connect())
	require.ErrorIs(t, owner.Disconnect(), ErrMountBusy)

	exists, err := nfs.IsMountExist(testNFSServerPath, reader.path)
	require.NoError(t, err)
	require.True(t, exists)

	require.NoError(t, reader.Disconnect())
	require.NoError(t, owner.Disconnect())

	exists, err = nfs.IsMountExist(testNFSServerPath, reader.path)
	require.NoError(t, err)
	require.False(t, exists)
}

// Connect is idempotent per client, so a client cannot leak a lease and leave the
// mount undeletable forever.
func TestNFSRepeatedConnectHoldsOneLease(t *testing.T) {
	reader, _ := newTestNFSRegistry(t)
	owner := &nfsFile{bentomlStore: bentomlStore{path: reader.path}, nfsServerPath: testNFSServerPath}

	require.NoError(t, reader.Connect())
	require.NoError(t, reader.Connect())
	require.NoError(t, reader.Disconnect())

	require.NoError(t, owner.Disconnect())
}

// The reported profile in miniature: rounds of three concurrent readers — a
// filtered list, an unfiltered list and a detail read — each connecting and
// disconnecting around its own request. Before the mount was leased, whichever
// finished first unmounted the tree the other two were reading, and the round
// came back with mount errors or an empty list.
func TestNFSConcurrentReadersNeverLoseTheMount(t *testing.T) {
	shared, _ := newTestNFSRegistry(t)
	writeStoredModel(t, shared.path, "qwen3", "v1", nil)

	read := func(t *testing.T, do func(*nfsFile) error) {
		t.Helper()

		client := &nfsFile{bentomlStore: bentomlStore{path: shared.path}, nfsServerPath: testNFSServerPath}
		require.NoError(t, client.Connect())

		defer func() { require.NoError(t, client.Disconnect()) }()

		require.NoError(t, do(client))
	}

	for round := range 100 {
		var wg sync.WaitGroup

		wg.Add(3)

		go func() {
			defer wg.Done()
			read(t, func(client *nfsFile) error {
				page, err := client.ListModels(ListOption{})
				if err == nil && len(page.Models) != 1 {
					err = errors.Errorf("round %d: unfiltered list returned %d models", round, len(page.Models))
				}

				return err
			})
		}()

		go func() {
			defer wg.Done()
			read(t, func(client *nfsFile) error {
				page, err := client.ListModels(ListOption{Search: "qwen"})
				if err == nil && len(page.Models) != 1 {
					err = errors.Errorf("round %d: filtered list returned %d models", round, len(page.Models))
				}

				return err
			})
		}()

		go func() {
			defer wg.Done()
			read(t, func(client *nfsFile) error {
				_, err := client.GetModelDetail("qwen3", "v1")
				return err
			})
		}()

		wg.Wait()
	}
}
