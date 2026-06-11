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
Grafana is provided by a dependency chart. Its Service name is derived from
the subchart's release context, not this parent chart's fullname.
*/}}
{{- define "neutree.grafana.serviceName" -}}
{{- $name := printf "%s-grafana" .Release.Name -}}
{{- if .Values.grafana.fullnameOverride -}}
{{- $name = (.Values.grafana.fullnameOverride | trunc 63 | trimSuffix "-") -}}
{{- end -}}
{{- default $name .Values.grafana.service.name -}}
{{- end -}}

{{/*
VictoriaMetrics is provided by a dependency chart. Its vminsert Service name is
derived from the subchart's release context, not this parent chart's fullname.
*/}}
{{- define "neutree.vminsert.serviceName" -}}
{{- $vminsert := index .Values "victoria-metrics-cluster" "vminsert" -}}
{{- $name := printf "%s-victoria-metrics-cluster-vminsert" .Release.Name -}}
{{- if $vminsert.fullnameOverride -}}
{{- $name = ($vminsert.fullnameOverride | trunc 63 | trimSuffix "-") -}}
{{- end -}}
{{- default $name $vminsert.service.name -}}
{{- end -}}

{{/*
Choose the Grafana URL exposed by the API system-info endpoint.
Priority:
1. system.grafana.url explicit override
2. grafana.ingress host when Grafana ingress is enabled
3. in-cluster Grafana Service URL
*/}}
{{- define "neutree.grafana.url" -}}
{{- $url := .Values.system.grafana.url -}}
{{- if and (not $url) .Values.grafana.enabled -}}
{{- if and .Values.grafana.ingress.enabled (gt (len .Values.grafana.ingress.hosts) 0) -}}
{{- $scheme := "http" -}}
{{- if gt (len .Values.grafana.ingress.tls) 0 -}}
{{- $scheme = "https" -}}
{{- end -}}
{{- $host := first .Values.grafana.ingress.hosts -}}
{{- $path := .Values.grafana.ingress.path | default "/" -}}
{{- if eq $path "/" -}}
{{- $url = printf "%s://%s" $scheme $host -}}
{{- else if hasPrefix "/" $path -}}
{{- $url = printf "%s://%s%s" $scheme $host $path -}}
{{- else -}}
{{- $url = printf "%s://%s/%s" $scheme $host $path -}}
{{- end -}}
{{- else -}}
{{- $url = printf "http://%s:80" (include "neutree.grafana.serviceName" .) -}}
{{- end -}}
{{- end -}}
{{- $url -}}
{{- end -}}
