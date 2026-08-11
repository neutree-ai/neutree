package metrics

import (
	"regexp"
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// The `neutree-inference` scrape job discovers inference pods by their
// `app=inference` label and, historically, scraped every one of them on a fixed
// :8000/metrics. Engine versions can now declare a metrics-export capability
// (see api/v1/engine_types.go), and these rules turn those declarations into
// relabel configuration.
//
// The filtering deliberately lives in the vmagent config rather than in pod
// metadata. Encoding the decision as a pod label or annotation would change the
// pod template, so upgrading Neutree would roll every inference deployment --
// minutes of downtime per endpoint while models reload -- and would leave
// already-running pods unfiltered until they happened to be redeployed. Pods
// already carry `engine` and `engine_version` labels, which is enough to target
// them from the config side with no pod change at all.
//
// Consistent with the capability protocol's forward-compatibility rule, this is
// an exclusion list, not an allow list: an engine version that declares nothing
// is absent from the rules and keeps being scraped exactly as before.

// InferenceScrapeExclusion identifies an engine version that declared it does
// not export metrics, so its pods are dropped from the inference job.
type InferenceScrapeExclusion struct {
	Engine  string
	Version string
}

// InferenceScrapeOverride redirects the inference job to a non-default metrics
// port or path for one engine version.
type InferenceScrapeOverride struct {
	Engine  string
	Version string

	// Port is set only when it differs from the job's default.
	Port int

	// Path is set only when it differs from the job's default.
	Path string
}

// HasPort reports whether this override changes the scrape port.
func (o InferenceScrapeOverride) HasPort() bool {
	return o.Port != 0
}

// HasPath reports whether this override changes the metrics path.
func (o InferenceScrapeOverride) HasPath() bool {
	return o.Path != ""
}

// TargetRegex renders the regex matching this override's engine and version
// against the `[engine, engine_version]` source labels, which relabel joins with
// the default ";" separator. Engine names and versions carry regex
// metacharacters ("v0.24.0"), so both halves are quoted.
func (o InferenceScrapeOverride) TargetRegex() string {
	return regexp.QuoteMeta(o.Engine) + ";" + regexp.QuoteMeta(o.Version)
}

// InferenceScrapeRules is the full set of inference-job adjustments derived from
// the engines available to a cluster.
type InferenceScrapeRules struct {
	Exclusions []InferenceScrapeExclusion
	Overrides  []InferenceScrapeOverride
}

// ExclusionRegex renders the alternation matching every excluded engine version
// against the `[engine, engine_version]` source labels. Returns "" when nothing
// is excluded, in which case the drop rule is omitted entirely rather than
// emitted with an empty regex -- an empty regex is anchored and would match
// every pod whose labels are also empty.
func (r InferenceScrapeRules) ExclusionRegex() string {
	if len(r.Exclusions) == 0 {
		return ""
	}

	alternatives := make([]string, 0, len(r.Exclusions))
	for _, e := range r.Exclusions {
		alternatives = append(alternatives, regexp.QuoteMeta(e.Engine)+";"+regexp.QuoteMeta(e.Version))
	}

	return strings.Join(alternatives, "|")
}

// BuildInferenceScrapeRules derives the inference-job rules from the engines
// registered in a workspace.
//
// Only declarations that differ from the job's defaults produce a rule, so a
// cluster running nothing but engines that declare the defaults (or declare
// nothing at all) renders byte-identical config to before this feature.
func BuildInferenceScrapeRules(engines []v1.Engine, defaultPort int, defaultPath string) InferenceScrapeRules {
	rules := InferenceScrapeRules{}

	for i := range engines {
		engine := engines[i]
		if engine.Spec == nil || engine.Metadata == nil {
			continue
		}

		for _, version := range engine.Spec.Versions {
			if version == nil || version.Version == "" {
				continue
			}

			resolved := version.ResolveMetricsExport()

			if !resolved.Enabled {
				rules.Exclusions = append(rules.Exclusions, InferenceScrapeExclusion{
					Engine:  engine.Metadata.Name,
					Version: version.Version,
				})

				// A dropped target needs no port or path override.
				continue
			}

			override := InferenceScrapeOverride{
				Engine:  engine.Metadata.Name,
				Version: version.Version,
			}

			if resolved.Port != defaultPort {
				override.Port = resolved.Port
			}

			if resolved.Path != defaultPath {
				override.Path = resolved.Path
			}

			if override.HasPort() || override.HasPath() {
				rules.Overrides = append(rules.Overrides, override)
			}
		}
	}

	// Stable ordering keeps the rendered config -- and therefore the config
	// hash that triggers a vmagent restart -- from churning on every reconcile,
	// since engine listing order is not guaranteed.
	sort.Slice(rules.Exclusions, func(i, j int) bool {
		if rules.Exclusions[i].Engine != rules.Exclusions[j].Engine {
			return rules.Exclusions[i].Engine < rules.Exclusions[j].Engine
		}

		return rules.Exclusions[i].Version < rules.Exclusions[j].Version
	})

	sort.Slice(rules.Overrides, func(i, j int) bool {
		if rules.Overrides[i].Engine != rules.Overrides[j].Engine {
			return rules.Overrides[i].Engine < rules.Overrides[j].Engine
		}

		return rules.Overrides[i].Version < rules.Overrides[j].Version
	})

	return rules
}
