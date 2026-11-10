package grafana

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type routerDashboard struct {
	UID        string `json:"uid"`
	Templating struct {
		List []struct {
			Name       string          `json:"name"`
			Type       string          `json:"type"`
			Hide       int             `json:"hide"`
			Multi      bool            `json:"multi"`
			IncludeAll bool            `json:"includeAll"`
			AllValue   string          `json:"allValue"`
			Refresh    int             `json:"refresh"`
			Definition string          `json:"definition"`
			Query      json.RawMessage `json:"query"`
			Current    struct {
				Value string `json:"value"`
			} `json:"current"`
		} `json:"list"`
	} `json:"templating"`
	Panels []struct {
		ID          int    `json:"id"`
		Title       string `json:"title"`
		Type        string `json:"type"`
		FieldConfig struct {
			Defaults struct {
				Unit string `json:"unit"`
			} `json:"defaults"`
		} `json:"fieldConfig"`
		Targets []struct {
			Expr         string `json:"expr"`
			LegendFormat string `json:"legendFormat"`
			Range        bool   `json:"range"`
		} `json:"targets"`
		GridPos struct {
			H int `json:"h"`
			W int `json:"w"`
			X int `json:"x"`
			Y int `json:"y"`
		} `json:"gridPos"`
		Options struct {
			Legend struct {
				Placement string `json:"placement"`
			} `json:"legend"`
		} `json:"options"`
	} `json:"panels"`
}

func loadRouterDashboard(t *testing.T) routerDashboard {
	t.Helper()

	data, err := os.ReadFile("dashboards/router_dashboard.json")
	require.NoError(t, err)
	var dashboard routerDashboard
	require.NoError(t, json.Unmarshal(data, &dashboard))
	require.Equal(t, "router", dashboard.UID)

	return dashboard
}

func TestRouterDashboardReplicaSelectors(t *testing.T) {
	dashboard := loadRouterDashboard(t)
	variables := map[string]bool{}
	for _, variable := range dashboard.Templating.List {
		variables[variable.Name] = true
		switch variable.Name {
		case "datasource":
			require.Equal(t, "datasource", variable.Type)
			require.Equal(t, "neutree-cluster", variable.Current.Value)
		case "Cluster":
			var query string
			require.NoError(t, json.Unmarshal(variable.Query, &query))
			require.Equal(t, variable.Definition, query)
			require.Equal(t, "query", variable.Type)
			require.True(t, variable.IncludeAll)
			require.Equal(t, 1, variable.Refresh)
			require.Contains(t, variable.Definition, "label_values(neutree_node_ready, neutree_cluster)")
		case "Workspace":
			require.Equal(t, 2, variable.Hide)
			require.False(t, variable.Multi)
			require.False(t, variable.IncludeAll)
		case "Endpoint", "Replicas":
			var query struct {
				Query string `json:"query"`
			}
			require.NoError(t, json.Unmarshal(variable.Query, &query))
			require.Equal(t, variable.Definition, query.Query)
			require.Equal(t, "query", variable.Type)
			require.Zero(t, variable.Hide)
			require.True(t, variable.Multi)
			require.True(t, variable.IncludeAll)
			require.Equal(t, ".*", variable.AllValue)
			require.Equal(t, "$__all", variable.Current.Value)
			require.NotZero(t, variable.Refresh)
			require.Contains(t, variable.Definition, "router:healthy_pods_total")
			require.Contains(t, variable.Definition, `job="neutree-router"`)
			require.Contains(t, variable.Definition, `neutree_cluster=~"${Cluster:regex}"`)
			require.Contains(t, variable.Definition, `workspace=~"${Workspace:regex}"`)
			if variable.Name == "Replicas" {
				require.Contains(t, variable.Definition, `endpoint=~"${Endpoint:regex}"`)
				require.Contains(t, variable.Definition, `replica!=""`)
				require.Regexp(t, `,\s*replica\)$`, variable.Definition)
			} else {
				require.Regexp(t, `,\s*endpoint\)$`, variable.Definition)
			}
		}
	}
	for _, name := range []string{"datasource", "Cluster", "Workspace", "Endpoint", "Replicas"} {
		require.True(t, variables[name], "missing %s variable", name)
	}
}

func TestRouterDashboardPanelColumns(t *testing.T) {
	dashboard := loadRouterDashboard(t)
	positions := map[int][4]int{}
	for _, panel := range dashboard.Panels {
		require.NotEqual(t, "Router Requests", panel.Title)
		require.NotContains(t, panel.Title, "Router")
		positions[panel.ID] = [4]int{panel.GridPos.X, panel.GridPos.Y, panel.GridPos.W, panel.GridPos.H}
	}
	require.Equal(t, [4]int{0, 1, 8, 8}, positions[1])
	require.Equal(t, [4]int{8, 1, 8, 8}, positions[3])
	require.Equal(t, [4]int{16, 1, 8, 8}, positions[19])
	require.Equal(t, [4]int{0, 9, 12, 8}, positions[20])
	require.Equal(t, [4]int{12, 9, 12, 8}, positions[21])
}

func TestRouterDashboardRequestQueries(t *testing.T) {
	dashboard := loadRouterDashboard(t)
	metrics := map[string]bool{}
	for _, panel := range dashboard.Panels {
		for _, target := range panel.Targets {
			expr := target.Expr
			for _, selector := range []string{
				`job="neutree-router"`,
				`neutree_cluster=~"${Cluster:regex}"`,
				`workspace=~"${Workspace:regex}"`,
				`endpoint=~"${Endpoint:regex}"`,
				`replica=~"${Replicas:regex}"`,
			} {
				require.Contains(t, expr, selector)
			}
			require.NotContains(t, expr, "vector(0)")
			switch {
			case strings.Contains(expr, "router:healthy_pods_total"):
				metrics["discovery"] = true
				require.Regexp(t, `count by\s*\(endpoint\)\s*\(max by\s*\(endpoint,\s*server\)`, expr)
				require.False(t, target.Range)
			case strings.Contains(expr, "router:current_qps"):
				metrics["qps"] = true
				require.Regexp(t, `^sum by\s*\(endpoint\)`, expr)
				require.NotContains(t, expr, "rate(")
				require.False(t, target.Range)
			case strings.Contains(expr, "router:avg_latency"):
				metrics["latency"] = true
				require.Regexp(t, `^avg by\s*\(endpoint\)\s*\(router:avg_latency\{`, expr)
				require.Equal(t, "s", panel.FieldConfig.Defaults.Unit)
				require.False(t, target.Range)
				require.Equal(t, "stat", panel.Type)
			case strings.Contains(expr, "router:connection_pool_waiting"):
				metrics["queue"] = true
				require.Regexp(t, `^sum by\s*\(endpoint,\s*replica\)`, expr)
				require.Equal(t, "{{replica}}", target.LegendFormat)
				require.Equal(t, "right", panel.Options.Legend.Placement)
				require.Equal(t, "timeseries", panel.Type)
			case strings.Contains(expr, "router:num_requests_running"):
				metrics["runtime"] = true
				require.Regexp(t, `^sum by\s*\(endpoint,\s*replica\)`, expr)
				require.Equal(t, "{{replica}}", target.LegendFormat)
				require.Equal(t, "right", panel.Options.Legend.Placement)
				require.Equal(t, "timeseries", panel.Type)
			default:
				t.Fatalf("unexpected Router request metric: %s", expr)
			}
			if panel.Type == "timeseries" &&
				!strings.Contains(expr, "router:connection_pool_waiting") &&
				!strings.Contains(expr, "router:num_requests_running") {
				for _, label := range []string{"endpoint", "replica", "server", "pod"} {
					require.Contains(t, target.LegendFormat, "{{"+label+"}}")
				}
			}
		}
	}
	for _, metric := range []string{"discovery", "qps", "latency", "queue", "runtime"} {
		require.True(t, metrics[metric], "missing %s panel", metric)
	}
}
