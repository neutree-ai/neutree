package launch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareCoreWiresKongMetrics(t *testing.T) {
	for _, remoteWrite := range []string{"", "http://metrics:8480/insert/0/prometheus/"} {
		t.Run(remoteWrite, func(t *testing.T) {
			dir := t.TempDir()
			err := prepareNeutreeCoreDeployConfig(neutreeCoreInstallOptions{
				commonOptions:         &commonOptions{workDir: dir, nodeIP: "192.0.2.1", deployType: "local", deployMode: "single"},
				jwtSecret:             "test-secret",
				version:               "v1.0.0",
				metricsRemoteWriteURL: remoteWrite,
			})
			require.NoError(t, err)
			root := filepath.Join(dir, "neutree-core")
			project, err := cli.ProjectFromOptions(&cli.ProjectOptions{ConfigPaths: []string{filepath.Join(root, "docker-compose.yml")}})
			require.NoError(t, err)
			foundAgent := false
			for _, service := range project.Services {
				switch service.Name {
				case "kong":
					require.NotNil(t, service.Environment["KONG_STATUS_LISTEN"])
					assert.Equal(t, "0.0.0.0:8100", *service.Environment["KONG_STATUS_LISTEN"])
					for _, port := range service.Ports {
						assert.NotEqualValues(t, 8100, port.Target, "metrics must not be published on the host")
					}
				case "vmagent":
					foundAgent = true
					require.NotNil(t, service.Environment["NEUTREE_KONG_METRICS_HOST"])
					assert.Equal(t, "kong", *service.Environment["NEUTREE_KONG_METRICS_HOST"])
				}
			}
			assert.Equal(t, remoteWrite != "", foundAgent)
			config, err := os.ReadFile(filepath.Join(root, "vmagent", "prometheus.yml"))
			require.NoError(t, err)
			assert.Contains(t, string(config), "metrics_path: /metrics/neutree")
			assert.Contains(t, string(config), "%{NEUTREE_KONG_METRICS_HOST}")
			for _, file := range []string{"metrics.lua", "status_api.lua"} {
				assert.FileExists(t, filepath.Join(root, "gateway", "kong", "plugins", "neutree-ai-gateway", file))
			}
		})
	}
}
