package featuregate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
)

// Feature is a string type for feature names.
type Feature string

// PreRelease indicates the maturity level of a feature.
type PreRelease string

const (
	// Alpha indicates that a feature is experimental and disabled by default.
	Alpha PreRelease = "ALPHA"
	// Beta indicates that a feature is pre-release and enabled by default.
	Beta PreRelease = "BETA"
	// GA indicates that a feature is generally available, always enabled, and cannot be disabled.
	GA PreRelease = ""
	// Deprecated: feature is deprecated and disabled by default.
	Deprecated PreRelease = "DEPRECATED"
)

// FeatureSpec represents the specification of a feature.
type FeatureSpec struct {
	// Default is the default enablement state for the feature.
	Default bool
	// PreRelease indicates the maturity level of the feature.
	PreRelease PreRelease
}

// FeatureGate provides read access to feature gates.
type FeatureGate interface {
	// Enabled returns true if the feature is enabled.
	Enabled(key Feature) bool
	// KnownFeatures returns a slice of strings describing known features.
	// Each entry has the format "FeatureName=true|false (ALPHA|BETA|GA|DEPRECATED - default=true|false)".
	KnownFeatures() []string
}

// MutableFeatureGate extends FeatureGate with methods to modify feature gates.
type MutableFeatureGate interface {
	FeatureGate
	// Add adds features to the feature gate. Returns an error if a feature is already registered
	// with a different spec.
	Add(features map[Feature]FeatureSpec) error
	// SetFromMap sets feature gate values from a map of feature name to boolean string.
	SetFromMap(m map[string]bool) error
	// AddFlag adds the --feature-gates flag to the given FlagSet.
	AddFlag(fs *pflag.FlagSet)
}

// featureGate implements MutableFeatureGate.
type featureGate struct {
	mu       sync.RWMutex
	known    map[Feature]FeatureSpec
	enabled  map[Feature]bool
	flagOnce sync.Once
}

// NewFeatureGate creates a new MutableFeatureGate.
func NewFeatureGate() MutableFeatureGate {
	return &featureGate{
		known:   make(map[Feature]FeatureSpec),
		enabled: make(map[Feature]bool),
	}
}

func (f *featureGate) Enabled(key Feature) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if val, ok := f.enabled[key]; ok {
		return val
	}

	if spec, ok := f.known[key]; ok {
		return spec.Default
	}

	klog.Warningf("Feature %q is not registered in the feature gate", key)

	return false
}

func (f *featureGate) KnownFeatures() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()

	var known []string
	for k, v := range f.known {
		pre := string(v.PreRelease)
		if pre == "" {
			pre = "GA"
		}

		known = append(known, fmt.Sprintf("%s=true|false (%s - default=%t)", k, pre, v.Default))
	}

	sort.Strings(known)

	return known
}

// validPreRelease contains the set of accepted PreRelease values.
var validPreRelease = map[PreRelease]bool{
	Alpha:      true,
	Beta:       true,
	GA:         true,
	Deprecated: true,
}

func (f *featureGate) Add(features map[Feature]FeatureSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for k, v := range features {
		if !validPreRelease[v.PreRelease] {
			return fmt.Errorf("feature gate %q has invalid PreRelease value %q (valid: Alpha, Beta, GA, Deprecated)", k, v.PreRelease)
		}

		if existing, ok := f.known[k]; ok {
			if existing != v {
				return fmt.Errorf("feature gate %q already registered with different spec", k)
			}

			continue
		}

		f.known[k] = v
	}

	return nil
}

func (f *featureGate) SetFromMap(m map[string]bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for k, v := range m {
		feature := Feature(k)

		spec, ok := f.known[feature]
		if !ok {
			return fmt.Errorf("unrecognized feature gate: %s", k)
		}

		if spec.PreRelease == GA && !v {
			return fmt.Errorf("feature gate %s is GA and cannot be disabled", k)
		}

		f.enabled[feature] = v
		klog.Infof("Feature gate %s=%t", k, v)
	}

	return nil
}

func (f *featureGate) AddFlag(fs *pflag.FlagSet) {
	f.flagOnce.Do(func() {
		fs.Var(&featureGateFlag{gate: f}, "feature-gates",
			"A set of key=value pairs that describe feature gates for alpha/beta features. "+
				"Options are:\n"+strings.Join(f.KnownFeatures(), "\n"))
	})
}

// featureGateFlag implements pflag.Value for parsing --feature-gates flag.
type featureGateFlag struct {
	gate *featureGate
}

func (fgf *featureGateFlag) String() string {
	fgf.gate.mu.RLock()
	defer fgf.gate.mu.RUnlock()

	var pairs []string
	for k, v := range fgf.gate.enabled {
		pairs = append(pairs, fmt.Sprintf("%s=%t", k, v))
	}

	sort.Strings(pairs)

	return strings.Join(pairs, ",")
}

func (fgf *featureGateFlag) Set(value string) error {
	m, err := parseFeatureGateValue(value)
	if err != nil {
		return err
	}

	return fgf.gate.SetFromMap(m)
}

func (fgf *featureGateFlag) Type() string {
	return "mapStringBool"
}

// parseFeatureGateValue parses a string of the form "key1=true,key2=false".
func parseFeatureGateValue(value string) (map[string]bool, error) {
	m := make(map[string]bool)

	for _, pair := range strings.Split(value, ",") {
		if pair == "" {
			continue
		}

		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid feature gate format: %q (expected key=value)", pair)
		}

		key := strings.TrimSpace(parts[0])

		val, err := strconv.ParseBool(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid value for feature gate %s: %q (expected true or false)", key, parts[1])
		}

		m[key] = val
	}

	return m, nil
}
