package metrics

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestSharedCacheDiscovery(t *testing.T) {
	config, err := renderKubernetesVMAgentConfig(MetricsManifestVariables{Namespace: "cluster-ns", ClusterName: "cluster-a", Workspace: "team-a"})
	require.NoError(t, err)
	var parsed struct {
		Jobs []struct {
			Name      string `yaml:"job_name"`
			Discovery []struct {
				Role       string `yaml:"role"`
				Namespaces struct {
					Names []string `yaml:"names"`
				} `yaml:"namespaces"`
			} `yaml:"kubernetes_sd_configs"`
			Rules []struct {
				Action      string `yaml:"action"`
				Regex       string `yaml:"regex"`
				Target      string `yaml:"target_label"`
				Replacement string `yaml:"replacement"`
			} `yaml:"relabel_configs"`
		} `yaml:"scrape_configs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(config), &parsed))
	found := false
	for _, job := range parsed.Jobs {
		if job.Name != "neutree-zcache" {
			continue
		}
		found = true
		require.Equal(t, "endpoints", job.Discovery[0].Role)
		require.Equal(t, []string{"cluster-ns"}, job.Discovery[0].Namespaces.Names)
		keep := regexp.MustCompile("^(?:" + job.Rules[0].Regex + ")$")
		for labels, expected := range map[string]bool{"true;lmcache;metrics": true, "true;node-agent;metrics": false, "true;lmcache;service": false, ";lmcache;metrics": false} {
			require.Equal(t, expected, keep.MatchString(labels), labels)
		}
		labels := map[string]string{}
		for _, rule := range job.Rules {
			labels[rule.Target] = rule.Replacement
		}
		require.Equal(t, "team-a", labels["workspace"])
		require.Equal(t, "cluster-a", labels["neutree_cluster"])
	}
	require.True(t, found)
}
