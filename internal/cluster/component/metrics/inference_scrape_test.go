package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func engineWithVersions(name string, versions ...*v1.EngineVersion) v1.Engine {
	return v1.Engine{
		Metadata: &v1.Metadata{Name: name},
		Spec:     &v1.EngineSpec{Versions: versions},
	}
}

func TestBuildInferenceScrapeRules(t *testing.T) {
	tests := []struct {
		name           string
		engines        []v1.Engine
		wantExclusions []InferenceScrapeExclusion
		wantOverrides  []InferenceScrapeOverride
	}{
		{
			name:    "no engines",
			engines: nil,
		},
		{
			// The forward-compat case: engines registered before the capability
			// protocol produce no rules at all, so the job keeps scraping them.
			name: "undeclared engines produce no rules",
			engines: []v1.Engine{
				engineWithVersions("vllm", &v1.EngineVersion{Version: "v0.24.0"}),
			},
		},
		{
			name: "declaring the defaults produces no rules",
			engines: []v1.Engine{
				engineWithVersions("vllm", &v1.EngineVersion{
					Version: "v0.24.0",
					Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{
							Enabled: true,
							Port:    v1.DefaultMetricsExportPort,
							Path:    v1.DefaultMetricsExportPath,
						},
					},
				}),
			},
		},
		{
			name: "disabled engine version is excluded",
			engines: []v1.Engine{
				engineWithVersions("mineru", &v1.EngineVersion{
					Version: "v1.0.0",
					Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{Enabled: false},
					},
				}),
			},
			wantExclusions: []InferenceScrapeExclusion{{Engine: "mineru", Version: "v1.0.0"}},
		},
		{
			name: "custom port and path become an override",
			engines: []v1.Engine{
				engineWithVersions("custom", &v1.EngineVersion{
					Version: "v2",
					Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{
							Enabled: true,
							Port:    9100,
							Path:    "/internal/metrics",
						},
					},
				}),
			},
			wantOverrides: []InferenceScrapeOverride{
				{Engine: "custom", Version: "v2", Port: 9100, Path: "/internal/metrics"},
			},
		},
		{
			// A disabled version is dropped outright, so it must not also emit a
			// port override that would never apply.
			name: "disabled wins over a custom port",
			engines: []v1.Engine{
				engineWithVersions("custom", &v1.EngineVersion{
					Version: "v2",
					Capabilities: &v1.EngineCapabilities{
						MetricsExport: &v1.MetricsExportCapability{Enabled: false, Port: 9100},
					},
				}),
			},
			wantExclusions: []InferenceScrapeExclusion{{Engine: "custom", Version: "v2"}},
		},
		{
			name: "versions of one engine are handled independently",
			engines: []v1.Engine{
				engineWithVersions("vllm",
					&v1.EngineVersion{Version: "v0.17.1"},
					&v1.EngineVersion{
						Version: "v0.24.0",
						Capabilities: &v1.EngineCapabilities{
							MetricsExport: &v1.MetricsExportCapability{Enabled: false},
						},
					},
				),
			},
			wantExclusions: []InferenceScrapeExclusion{{Engine: "vllm", Version: "v0.24.0"}},
		},
		{
			name: "malformed entries are skipped",
			engines: []v1.Engine{
				{Metadata: &v1.Metadata{Name: "no-spec"}},
				engineWithVersions("nil-version", nil),
				engineWithVersions("blank-version", &v1.EngineVersion{Version: ""}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildInferenceScrapeRules(tt.engines, v1.DefaultMetricsExportPort, v1.DefaultMetricsExportPath)

			assert.Equal(t, tt.wantExclusions, got.Exclusions)
			assert.Equal(t, tt.wantOverrides, got.Overrides)
		})
	}
}

// TestBuildInferenceScrapeRules_StableOrder pins the ordering guarantee: engine
// listing order is not deterministic, and an unstable rule order would change
// the rendered config on every reconcile, restarting vmagent each time.
func TestBuildInferenceScrapeRules_StableOrder(t *testing.T) {
	disabled := func(version string) *v1.EngineVersion {
		return &v1.EngineVersion{
			Version: version,
			Capabilities: &v1.EngineCapabilities{
				MetricsExport: &v1.MetricsExportCapability{Enabled: false},
			},
		}
	}

	forward := BuildInferenceScrapeRules([]v1.Engine{
		engineWithVersions("zeta", disabled("v2"), disabled("v1")),
		engineWithVersions("alpha", disabled("v1")),
	}, v1.DefaultMetricsExportPort, v1.DefaultMetricsExportPath)

	reversed := BuildInferenceScrapeRules([]v1.Engine{
		engineWithVersions("alpha", disabled("v1")),
		engineWithVersions("zeta", disabled("v1"), disabled("v2")),
	}, v1.DefaultMetricsExportPort, v1.DefaultMetricsExportPath)

	assert.Equal(t, forward.Exclusions, reversed.Exclusions)
	assert.Equal(t, []InferenceScrapeExclusion{
		{Engine: "alpha", Version: "v1"},
		{Engine: "zeta", Version: "v1"},
		{Engine: "zeta", Version: "v2"},
	}, forward.Exclusions)
}

func TestInferenceScrapeRules_ExclusionRegex(t *testing.T) {
	assert.Equal(t, "", InferenceScrapeRules{}.ExclusionRegex(),
		"an empty regex is anchored and would match pods with empty labels, so it must be omitted instead")

	// Versions contain dots, which are regex metacharacters: unquoted, "v0.24.0"
	// would also match "v0x24y0".
	rules := InferenceScrapeRules{Exclusions: []InferenceScrapeExclusion{
		{Engine: "vllm", Version: "v0.24.0"},
		{Engine: "mineru", Version: "v1.0.0"},
	}}
	assert.Equal(t, `vllm;v0\.24\.0|mineru;v1\.0\.0`, rules.ExclusionRegex())
}

// renderJob renders the full vmagent config and returns one scrape job, parsed
// as YAML. Parsing rather than string matching is the point: a template that
// emits subtly malformed YAML would leave vmagent scraping nothing, and a
// string assertion would not notice.
func renderJob(t *testing.T, rules InferenceScrapeRules, jobName string) map[string]interface{} {
	t.Helper()

	rendered, err := renderKubernetesVMAgentConfig(MetricsManifestVariables{
		ClusterName:          "test-cluster",
		Workspace:            "default",
		Namespace:            "neutree",
		InferenceDefaultPort: v1.DefaultMetricsExportPort,
		InferenceScrapeRules: rules,
	})
	require.NoError(t, err)

	var config struct {
		ScrapeConfigs []map[string]interface{} `yaml:"scrape_configs"`
	}

	require.NoError(t, yaml.Unmarshal([]byte(rendered), &config), "rendered config is not valid YAML:\n%s", rendered)

	for _, job := range config.ScrapeConfigs {
		if job["job_name"] == jobName {
			return job
		}
	}

	t.Fatalf("%s job not found in rendered config:\n%s", jobName, rendered)

	return nil
}

func renderInferenceJob(t *testing.T, rules InferenceScrapeRules) map[string]interface{} {
	t.Helper()

	return renderJob(t, rules, "neutree-inference")
}

func relabelConfigs(t *testing.T, job map[string]interface{}) []map[string]interface{} {
	t.Helper()

	raw, ok := job["relabel_configs"].([]interface{})
	require.True(t, ok, "relabel_configs missing or not a list")

	configs := make([]map[string]interface{}, 0, len(raw))

	for _, entry := range raw {
		cfg, ok := entry.(map[string]interface{})
		require.True(t, ok, "relabel entry is not a mapping")
		configs = append(configs, cfg)
	}

	return configs
}

// TestRenderInferenceJob_NoRules is the forward-compat guard at the config
// level: with no declarations, the job must keep the fixed :8000 target and
// carry no drop rule at all.
func TestRenderInferenceJob_NoRules(t *testing.T) {
	job := renderInferenceJob(t, InferenceScrapeRules{})

	for _, cfg := range relabelConfigs(t, job) {
		assert.NotEqual(t, "drop", cfg["action"], "no engine declared anything, so nothing may be dropped")
	}

	assert.Contains(t, relabelReplacements(t, job), "$1:8000")
}

func relabelReplacements(t *testing.T, job map[string]interface{}) []string {
	t.Helper()

	replacements := []string{}

	for _, cfg := range relabelConfigs(t, job) {
		if replacement, ok := cfg["replacement"].(string); ok {
			replacements = append(replacements, replacement)
		}
	}

	return replacements
}

func TestRenderInferenceJob_Exclusions(t *testing.T) {
	job := renderInferenceJob(t, InferenceScrapeRules{
		Exclusions: []InferenceScrapeExclusion{{Engine: "mineru", Version: "v1.0.0"}},
	})

	var dropRules []map[string]interface{}

	for _, cfg := range relabelConfigs(t, job) {
		if cfg["action"] == "drop" {
			dropRules = append(dropRules, cfg)
		}
	}

	require.Len(t, dropRules, 1)
	assert.Equal(t, `mineru;v1\.0\.0`, dropRules[0]["regex"])
	assert.Equal(t,
		[]interface{}{"__meta_kubernetes_pod_label_engine", "__meta_kubernetes_pod_label_engine_version"},
		dropRules[0]["source_labels"])
}

func TestRenderInferenceJob_Overrides(t *testing.T) {
	job := renderInferenceJob(t, InferenceScrapeRules{
		Overrides: []InferenceScrapeOverride{
			{Engine: "custom", Version: "v2", Port: 9100, Path: "/internal/metrics"},
		},
	})

	configs := relabelConfigs(t, job)

	defaultAddressIdx, overrideAddressIdx, pathIdx := -1, -1, -1

	for i, cfg := range configs {
		switch {
		case cfg["target_label"] == "__address__" && cfg["replacement"] == "$1:8000":
			defaultAddressIdx = i
		case cfg["target_label"] == "__address__" && cfg["replacement"] == "$1:9100":
			overrideAddressIdx = i
		case cfg["target_label"] == "__metrics_path__":
			pathIdx = i
		}
	}

	require.NotEqual(t, -1, defaultAddressIdx, "default address rule missing")
	require.NotEqual(t, -1, overrideAddressIdx, "override address rule missing")
	require.NotEqual(t, -1, pathIdx, "metrics path override missing")

	// Relabel rules apply in order and the override rewrites the same target
	// label, so it only takes effect if it comes after the default.
	assert.Greater(t, overrideAddressIdx, defaultAddressIdx,
		"the port override must follow the default address rule to win")

	assert.Equal(t, `(.+);custom;v2`, configs[overrideAddressIdx]["regex"])
	assert.Equal(t, "/internal/metrics", configs[pathIdx]["replacement"])
	assert.Equal(t, `custom;v2`, configs[pathIdx]["regex"])
}

// TestRenderInferenceJob_OtherJobsUnaffected guards the blast radius: the
// router job shares the inference job's shape, and an over-broad template edit
// would silently change it too.
func TestRenderInferenceJob_OtherJobsUnaffected(t *testing.T) {
	routerJob := renderJob(t, InferenceScrapeRules{
		Exclusions: []InferenceScrapeExclusion{{Engine: "mineru", Version: "v1.0.0"}},
	}, "neutree-router")

	for _, cfg := range relabelConfigs(t, routerJob) {
		assert.NotEqual(t, "drop", cfg["action"], "the router job must not inherit inference drop rules")
	}

	assert.Contains(t, relabelReplacements(t, routerJob), "$1:8000",
		"the router job keeps its own fixed address rule")
}
