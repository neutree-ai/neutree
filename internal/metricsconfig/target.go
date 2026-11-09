package metricsconfig

import (
	"fmt"
	"maps"
	"sort"
	"sync"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
	"gopkg.in/yaml.v3"
)

// Target declares endpoint discovery and producer-owned relabeling without
// interpreting the component or accelerator that exposes those metrics.
type Target struct {
	Owner          string
	JobName        string
	Namespace      string
	Selector       map[string]string
	StaticTargets  []string
	KubernetesRole string
	Scheme         string
	Port           int
	MetricsPath    string
	RelabelConfigs []promrelabel.RelabelConfig
}

// Snapshot is an isolated view of all targets registered for one reconcile.
type Snapshot struct {
	Targets []Target
}

// Registry collects explicit target declarations. Workload rendering remains
// with each producer; the registry only aggregates scrape configuration.
type Registry interface {
	Replace(owner string, targets []Target) error
	Remove(owner string)
	Snapshot() (Snapshot, error)
}

// TargetRegistry is reconstructed for each reconcile and is not persisted.
// Its zero value is ready for use.
type TargetRegistry struct {
	mu     sync.RWMutex
	owners map[string][]Target
}

func NewRegistry() *TargetRegistry {
	return &TargetRegistry{}
}

func (r *TargetRegistry) Replace(owner string, targets []Target) error {
	if r == nil || owner == "" {
		return fmt.Errorf("metrics target registry and owner are required")
	}

	copied := make([]Target, 0, len(targets))

	for _, target := range targets {
		cloned, err := cloneTarget(target)
		if err != nil {
			return fmt.Errorf("replace metrics targets for %s: %w", owner, err)
		}

		cloned.Owner = owner
		copied = append(copied, cloned)
	}

	sort.SliceStable(copied, func(i, j int) bool { return copied[i].JobName < copied[j].JobName })

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(copied) == 0 {
		delete(r.owners, owner)
	} else {
		if r.owners == nil {
			r.owners = make(map[string][]Target)
		}

		r.owners[owner] = copied
	}

	return nil
}

func (r *TargetRegistry) Remove(owner string) {
	if r == nil || owner == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.owners, owner)
}

func (r *TargetRegistry) Snapshot() (Snapshot, error) {
	if r == nil {
		return Snapshot{}, nil
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
			cloned, err := cloneTarget(target)
			if err != nil {
				return Snapshot{}, fmt.Errorf("snapshot metrics targets for %s: %w", owner, err)
			}

			targets = append(targets, cloned)
		}
	}

	return Snapshot{Targets: targets}, nil
}

func cloneTarget(target Target) (Target, error) {
	target.Selector = maps.Clone(target.Selector)
	target.StaticTargets = append([]string(nil), target.StaticTargets...)

	if len(target.RelabelConfigs) == 0 {
		target.RelabelConfigs = nil
		return target, nil
	}
	// The SDK DTO includes mutable regex and private If-expression state; its
	// YAML contract copies both without depending on those implementation fields.
	data, err := yaml.Marshal(target.RelabelConfigs)
	if err != nil {
		return Target{}, fmt.Errorf("marshal relabel configs for job %s: %w", target.JobName, err)
	}

	var relabelConfigs []promrelabel.RelabelConfig
	if err := yaml.Unmarshal(data, &relabelConfigs); err != nil {
		return Target{}, fmt.Errorf("clone relabel configs for job %s: %w", target.JobName, err)
	}

	if _, err := promrelabel.ParseRelabelConfigs(relabelConfigs); err != nil {
		return Target{}, fmt.Errorf("validate relabel configs for job %s: %w", target.JobName, err)
	}

	target.RelabelConfigs = relabelConfigs

	return target, nil
}
