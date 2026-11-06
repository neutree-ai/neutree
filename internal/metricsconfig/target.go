package metricsconfig

import (
	"maps"
	"sort"
	"sync"
)

// TargetKind identifies how a target is represented in the generated vmagent
// configuration.
type TargetKind string

const (
	TargetKindAcceleratorExporter   TargetKind = "accelerator-exporter"
	TargetKindVirtualizationMonitor TargetKind = "virtualization-monitor"
	TargetKindNodeAgent             TargetKind = "node-agent"
)

// Target is a backend-neutral scrape declaration. Kubernetes and static
// backends translate the discovery fields into their own vmagent syntax.
type Target struct {
	Owner           string
	AcceleratorType string
	JobName         string
	Kind            TargetKind
	Namespace       string
	Selector        map[string]string
	StaticTargets   []string
	Port            int
	MetricsPath     string
	Labels          map[string]string
}

// Snapshot is an immutable view of all targets registered for one reconcile.
type Snapshot struct {
	Targets []Target
}

// Registry collects target declarations from public components and
// AcceleratorOperators before MetricsComponent renders the final config.
type Registry interface {
	Replace(owner string, targets []Target)
	Remove(owner string)
	Snapshot() Snapshot
}

// TargetRegistry is an in-memory, per-reconcile registry. It is intentionally
// not persisted: the next reconcile reconstructs the complete desired target
// set and naturally removes stale owners.
type TargetRegistry struct {
	mu     sync.RWMutex
	owners map[string][]Target
}

func NewRegistry() *TargetRegistry {
	return &TargetRegistry{owners: map[string][]Target{}}
}

func (r *TargetRegistry) Replace(owner string, targets []Target) {
	if r == nil || owner == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(targets) == 0 {
		delete(r.owners, owner)
		return
	}

	copied := make([]Target, 0, len(targets))

	for _, target := range targets {
		target.Owner = owner
		target.Selector = maps.Clone(target.Selector)
		target.Labels = maps.Clone(target.Labels)
		target.StaticTargets = append([]string{}, target.StaticTargets...)
		copied = append(copied, target)
	}

	sort.SliceStable(copied, func(i, j int) bool {
		if copied[i].JobName == copied[j].JobName {
			return copied[i].AcceleratorType < copied[j].AcceleratorType
		}

		return copied[i].JobName < copied[j].JobName
	})

	r.owners[owner] = copied
}

func (r *TargetRegistry) Remove(owner string) {
	if r == nil || owner == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owners, owner)
}

func (r *TargetRegistry) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	owners := make([]string, 0, len(r.owners))
	for owner := range r.owners {
		owners = append(owners, owner)
	}

	sort.Strings(owners)

	targets := make([]Target, 0)

	for _, owner := range owners {
		for _, target := range r.owners[owner] {
			target.Selector = maps.Clone(target.Selector)
			target.Labels = maps.Clone(target.Labels)
			target.StaticTargets = append([]string{}, target.StaticTargets...)
			targets = append(targets, target)
		}
	}

	return Snapshot{Targets: targets}
}
