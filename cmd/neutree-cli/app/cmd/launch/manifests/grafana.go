package manifests

import (
	"embed"
)

// ray dashboard
//
//go:embed dashboards.tar
var GrafanaDashboardContent embed.FS

var GrafanaDatasource = `
apiVersion: 1

datasources:
    - name: neutree-cluster
      type: prometheus
      url: http://{{ .Ip}}:8481/select/0/prometheus
      isDefault: true
      jsonData:
        prometheusType: Prometheus
        prometheusVersion: 2.24.0
`

var GrafanaConfig = `
[auth.anonymous]
enabled = true
[dashboards]
# Path to the default home dashboard. If this value is empty, then Grafana uses StaticRootPath + "dashboards/home.json"
default_home_dashboard_path = /var/lib/grafana/dashboards/default_grafana_dashboard.json
[analytics]
check_for_plugin_updates = false
check_for_updates = false
enabled = false
reporting_enabled = false
[plugins]
public_key_retrieval_disabled = true
[security]
preinstall_disabled = true
allow_embedding = true
`

var GrafanaDashboardConfig = `
apiVersion: 1

providers:
- name: Prometheus
  orgId: 1
  folder: ''
  type: file
  options:
    path: /var/lib/grafana/dashboards
`

var GrafanaDockerComposeManifests = `
version: "3.8"

services:
  grafana:
    container_name: grafana
    image: grafana/grafana:{{.GrafanaVersion}}
    ports:
      - 3000:3000
    restart: always
    volumes:
      - grafanadata:/var/lib/grafana
      - {{ .WorkDir}}/provisioning/datasources/cluster.yml:/etc/grafana/provisioning/datasources/cluster.yml
      - {{ .WorkDir}}/provisioning/dashboards:/etc/grafana/provisioning/dashboards
      - {{ .WorkDir}}/dashboards:/var/lib/grafana/dashboards
      - {{ .WorkDir}}/grafana.ini:/etc/grafana/grafana.ini

volumes:
  grafanadata: {}
`
