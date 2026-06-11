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
Choose the Grafana URL exposed by the API system-info endpoint.
Priority:
1. system.grafana.url explicit override
2. grafana.grafana.ini.server.root_url
3. grafana.ingress host when Grafana ingress is enabled
No in-cluster fallback is used because this URL is consumed as a user-facing
external address.
*/}}
{{- define "neutree.grafana.url" -}}
{{- $url := .Values.system.grafana.url -}}
{{- if and (not $url) .Values.grafana.enabled -}}
{{- $grafanaRootURL := dig "grafana.ini" "server" "root_url" "" .Values.grafana -}}
{{- if $grafanaRootURL -}}
{{- $url = tpl (toString $grafanaRootURL) . -}}
{{- else -}}
{{- if and .Values.grafana.ingress.enabled (gt (len .Values.grafana.ingress.hosts) 0) -}}
{{- $scheme := "http" -}}
{{- if gt (len .Values.grafana.ingress.tls) 0 -}}
{{- $scheme = "https" -}}
{{- end -}}
{{- $host := tpl (toString (first .Values.grafana.ingress.hosts)) . -}}
{{- $path := .Values.grafana.ingress.path | default "/" -}}
{{- if eq $path "/" -}}
{{- $url = printf "%s://%s" $scheme $host -}}
{{- else if hasPrefix "/" $path -}}
{{- $url = printf "%s://%s%s" $scheme $host $path -}}
{{- else -}}
{{- $url = printf "%s://%s/%s" $scheme $host $path -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $url -}}
{{- end -}}
