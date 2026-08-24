package nfs

import (
	"sync"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"
)

// ErrMountBusy says a mount point still has active readers, so tearing it down
// was refused. It is transient by nature: the caller either retries later or
// leaves the mount to whoever is using it.
var ErrMountBusy = errors.New("nfs mount is still in use")

// ErrUnexpectedMountDevice says the mount point is occupied by a mount of some
// other export. Usually that is a registry whose URL was edited: the mount point
// is derived from the registry's identity, not its URL, so the old export is
// still sitting there.
var ErrUnexpectedMountDevice = errors.New("nfs mount point holds an unexpected device")

// A single NFS registry is read through one fixed mount point by every request
// handler and by the reconcile loop at the same time. Without a shared record of
// who is using it, each of them mounts and unmounts on its own schedule, and one
// finishing a read removes the mount another is still walking — which is how
// concurrent list/detail requests turned into mount ENOENT errors and wrongly
// empty listings.
//
// mountGuard is that shared record, keyed by mount point:
//   - opMu serialises the mount and unmount syscalls, so two callers never race
//     to create or remove the same mount point.
//   - refs counts the leases handed out by AcquireMount. Unmount refuses while it
//     is above zero.
//
// This is process-local. It makes concurrent readers inside one process safe
// against each other, which is where the damage was measured; a mount shared
// across processes still relies on each process only tearing down what it owns.
type mountGuard struct {
	opMu sync.Mutex

	refMu sync.Mutex
	refs  int
}

func (g *mountGuard) held() int {
	g.refMu.Lock()
	defer g.refMu.Unlock()

	return g.refs
}

func (g *mountGuard) acquire() {
	g.refMu.Lock()
	defer g.refMu.Unlock()

	g.refs++
}

func (g *mountGuard) release() {
	g.refMu.Lock()
	defer g.refMu.Unlock()

	if g.refs > 0 {
		g.refs--
	}
}

var (
	guardsMu sync.Mutex
	// guards is never pruned. It holds one small struct per mount point the
	// process has touched, which is bounded by the number of registries, and
	// keeping entries alive means a lease and a teardown racing over the same path
	// always meet on the same guard.
	guards = map[string]*mountGuard{}
)

func guardFor(mountPoint string) *mountGuard {
	guardsMu.Lock()
	defer guardsMu.Unlock()

	guard, ok := guards[mountPoint]
	if !ok {
		guard = &mountGuard{}
		guards[mountPoint] = guard
	}

	return guard
}

// AcquireMount mounts device at mountPoint if needed and records that the caller
// is reading through it. Every successful call must be paired with ReleaseMount,
// usually deferred. While a lease is held, Unmount on that mount point fails with
// ErrMountBusy instead of pulling the ground out from under the reader.
func AcquireMount(device string, mountPoint string) error {
	guard := guardFor(mountPoint)

	guard.opMu.Lock()
	defer guard.opMu.Unlock()

	if err := mountLocked(device, mountPoint); err != nil {
		// A leftover mount of a different export would otherwise wedge the mount
		// point for the life of the process, now that reads no longer unmount on
		// their way out. Replacing it is only safe because no lease is held: nobody
		// is reading through the mount being removed.
		if !errors.Is(err, ErrUnexpectedMountDevice) || guard.held() > 0 {
			return err
		}

		klog.Warningf("replacing stale mount at %s with %s: %v", mountPoint, device, err)

		if unmountErr := unmountLocked(mountPoint); unmountErr != nil {
			return errors.Wrapf(unmountErr, "failed to clear stale mount at %s", mountPoint)
		}

		if err = mountLocked(device, mountPoint); err != nil {
			return err
		}
	}

	guard.acquire()

	return nil
}

// ReleaseMount gives up one lease. It deliberately does not unmount when the last
// one goes: the mount belongs to the registry's lifecycle, and a read finishing
// is not a reason to tear it down — that is the unmount storm this whole
// mechanism exists to stop. Teardown is Unmount, called from the paths that own
// the registry.
func ReleaseMount(mountPoint string) {
	guardFor(mountPoint).release()
}
