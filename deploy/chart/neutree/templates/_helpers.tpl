{{/*
Expand the name of the chart.
*/}}
{{- define "neutree.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "neutree.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "neutree.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "neutree.labels" -}}
helm.sh/chart: {{ include "neutree.chart" . }}
{{ include "neutree.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "neutree.selectorLabels" -}}
app.kubernetes.io/name: {{ include "neutree.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "neutree.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "neutree.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Construct the image registry to use.
If global.image.registry is set, use it; otherwise use the component-specific registry.
*/}}
{{- define "neutree.imageRegistry" -}}
{{- if .global.image.registry -}}
{{- .global.image.registry -}}
{{- else -}}
{{- .componentRegistry -}}
{{- end -}}
{{- end -}}

{{/*
Construct the full image name.
Usage: include "neutree.image" (dict "global" .Values.global "componentRegistry" .Values.component.image.registry "repository" .Values.component.image.repository "tag" .Values.component.image.tag "defaultTag" .Chart.AppVersion)
*/}}
{{- define "neutree.image" -}}
{{- $registry := include "neutree.imageRegistry" . -}}
{{- $repository := .repository -}}
{{- $tag := .tag | default .defaultTag -}}
{{- if $registry -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- else -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end -}}
{{- end -}}

{{/*
Get the JWT secret with validation.
*/}}
{{- define "neutree.jwtSecret" -}}
{{- required "jwtSecret is required. Please set it in values.yaml or via --set jwtSecret=<your-secret>" .Values.jwtSecret -}}
{{- end -}}

{{/*
Resolve the AI inference trace store (VictoriaLogs) base URL.
Returns the in-cluster VictoriaLogs service when victorialogs.enabled is true,
otherwise the externally configured system.aiTraceStoreUrl. Empty when neither
is set — callers treat the empty result as "AI inference trace disabled".
*/}}
{{- define "neutree.aiTraceStoreUrl" -}}
{{- if .Values.victorialogs.enabled -}}
{{- printf "http://%s-victorialogs-service:9428" (include "neutree.fullname" .) -}}
{{- else if .Values.system.aiTraceStoreUrl -}}
{{- /* Trim a trailing slash so callers (e.g. the Vector sink) never render a
       double-slash path like https://host//insert/... */ -}}
{{- .Values.system.aiTraceStoreUrl | trimSuffix "/" -}}
{{- end -}}
{{- end -}}

{{/*
Choose the Kong proxy URL used as the user-facing inference endpoint base URL.
When Kong Ingress is enabled this must be the external Ingress URL. Otherwise
the value remains the in-cluster Service URL, which neutree-core may transform
through Kubernetes Service discovery for LoadBalancer installs.
*/}}
{{- define "neutree.kong.proxyUrl" -}}
{{- $url := printf "http://%s-kong-proxy:80" (include "neutree.fullname" .) -}}
{{- if and .Values.kong.ingress.enabled .Values.kong.ingress.host -}}
{{- $scheme := "http" -}}
{{- if gt (len .Values.kong.ingress.tls) 0 -}}
{{- $scheme = "https" -}}
{{- end -}}
{{- $host := tpl (toString .Values.kong.ingress.host) . -}}
{{- $path := .Values.kong.ingress.path | default "/" -}}
{{- if eq $path "/" -}}
{{- $url = printf "%s://%s" $scheme $host -}}
{{- else if hasPrefix "/" $path -}}
{{- $url = printf "%s://%s%s" $scheme $host $path | trimSuffix "/" -}}
{{- else -}}
{{- $url = printf "%s://%s/%s" $scheme $host $path | trimSuffix "/" -}}
{{- end -}}
{{- end -}}
{{- $url -}}
{{- end -}}

{{/*
VictoriaMetrics is provided by a dependency chart. Its vminsert Service name is
derived from the subchart's release context, not this parent chart's fullname.
*/}}
{{- define "neutree.vminsert.serviceName" -}}
{{- $vm := index .Values "victoria-metrics-cluster" -}}
{{- $vminsert := index .Values "victoria-metrics-cluster" "vminsert" -}}
{{- $name := "" -}}
{{- if $vminsert.fullnameOverride -}}
{{- $name = tpl $vminsert.fullnameOverride . -}}
{{- else -}}
{{- $base := "" -}}
{{- if $vm.fullnameOverride -}}
{{- $base = tpl $vm.fullnameOverride . -}}
{{- else if .Values.global.fullnameOverride -}}
{{- $base = tpl .Values.global.fullnameOverride . -}}
{{- else -}}
{{- $chartName := default "victoria-metrics-cluster" $vm.nameOverride -}}
{{- if contains $chartName .Release.Name -}}
{{- $base = .Release.Name -}}
{{- else -}}
{{- $base = printf "%s-%s" .Release.Name $chartName -}}
{{- end -}}
{{- end -}}
{{- $name = printf "%s-vminsert" $base -}}
{{- end -}}
{{- if or .Values.global.disableNameTruncation $vm.disableNameTruncation -}}
{{- $name -}}
{{- else -}}
{{- $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Choose the metrics remote-write URL for neutree-core and vmagent.
Priority:
1. metrics.remoteWriteUrl explicit override
2. VMInsert Ingress external URL when enabled
3. in-cluster VMInsert Service URL when the dependency is enabled
*/}}
{{- define "neutree.metricsRemoteWriteUrl" -}}
{{- $vmEnabled := index .Values "victoria-metrics-cluster" "enabled" -}}
{{- $vminsert := index .Values "victoria-metrics-cluster" "vminsert" -}}
{{- $url := .Values.metrics.remoteWriteUrl -}}
{{- if not $url -}}
{{- if and $vmEnabled $vminsert.ingress.enabled (gt (len $vminsert.ingress.hosts) 0) -}}
{{- $scheme := "http" -}}
{{- if gt (len $vminsert.ingress.tls) 0 -}}
{{- $scheme = "https" -}}
{{- end -}}
{{- $hostConfig := first $vminsert.ingress.hosts -}}
{{- if not (hasKey $hostConfig "name") -}}
{{- fail "victoria-metrics-cluster.vminsert.ingress.enabled is true but victoria-metrics-cluster.vminsert.ingress.hosts[0].name is missing" -}}
{{- end -}}
{{- $rawHost := get $hostConfig "name" -}}
{{- if not $rawHost -}}
{{- fail "victoria-metrics-cluster.vminsert.ingress.enabled is true but victoria-metrics-cluster.vminsert.ingress.hosts[0].name is empty" -}}
{{- end -}}
{{- $host := tpl (toString $rawHost) . -}}
{{- if not $host -}}
{{- fail "victoria-metrics-cluster.vminsert.ingress.enabled is true but victoria-metrics-cluster.vminsert.ingress.hosts[0].name renders empty" -}}
{{- end -}}
{{- $path := "/insert" -}}
{{- $pathValue := get $hostConfig "path" -}}
{{- if kindIs "slice" $pathValue -}}
{{- if gt (len $pathValue) 0 -}}
{{- $path = toString (first $pathValue) -}}
{{- end -}}
{{- else if kindIs "string" $pathValue -}}
{{- if $pathValue -}}
{{- $path = $pathValue -}}
{{- end -}}
{{- end -}}
{{- if eq $path "/" -}}
{{- $url = printf "%s://%s/insert/0/prometheus/" $scheme $host -}}
{{- else if hasPrefix "/" $path -}}
{{- $url = printf "%s://%s%s/0/prometheus/" $scheme $host ($path | trimSuffix "/") -}}
{{- else -}}
{{- $url = printf "%s://%s/%s/0/prometheus/" $scheme $host ($path | trimSuffix "/") -}}
{{- end -}}
{{- else if $vmEnabled -}}
{{- $url = printf "http://%s:8480/insert/0/prometheus/" (include "neutree.vminsert.serviceName" .) -}}
{{- end -}}
{{- end -}}
{{- $url -}}
{{- end -}}

{{/*
Grafana is provided by a dependency chart. Its Service name is derived from
the subchart's release context, not this parent chart's fullname.
*/}}
{{- define "neutree.grafana.serviceName" -}}
{{- $grafana := .Values.grafana -}}
{{- if $grafana.fullnameOverride -}}
{{- tpl $grafana.fullnameOverride . | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "grafana" $grafana.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Choose the Grafana URL exposed by the API system-info endpoint.
Priority:
1. system.grafana.url explicit override
2. grafana.grafana.ini.server.root_url
3. in-cluster Grafana Service URL when the dependency is enabled
*/}}
{{- define "neutree.grafana.url" -}}
{{- $url := .Values.system.grafana.url -}}
{{- if and (not $url) .Values.grafana.enabled -}}
{{- $grafanaRootURL := dig "grafana.ini" "server" "root_url" "" .Values.grafana -}}
{{- if $grafanaRootURL -}}
{{- $url = tpl (toString $grafanaRootURL) . -}}
{{- else if .Values.grafana.service.enabled -}}
{{- $port := .Values.grafana.service.port | default 80 -}}
{{- $url = printf "http://%s:%v" (include "neutree.grafana.serviceName" .) $port -}}
{{- end -}}
{{- end -}}
{{- $url -}}
{{- end -}}
