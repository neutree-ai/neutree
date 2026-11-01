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
Validate the Kong Nginx worker process value shared by every Kong workload.
*/}}
{{- define "neutree.kong.workerProcesses" -}}
{{- $value := toString .Values.kong.workerProcesses -}}
{{- if not (regexMatch "^(auto|[1-9][0-9]*)$" $value) -}}
{{- fail (printf "kong.workerProcesses must be \"auto\" or a positive integer (got %q)" $value) -}}
{{- end -}}
{{- $value -}}
{{- end -}}

{{- define "neutree.metricsQueryUrl" -}}
{{- if .Values.metrics.queryUrl -}}
{{- .Values.metrics.queryUrl | trimSuffix "/" -}}
{{- else if index .Values "victoria-metrics-cluster" "enabled" -}}
{{- printf "http://%s-victoria-metrics-cluster-vmselect:8481/select/0/prometheus" (include "neutree.fullname" .) -}}
{{- end -}}
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
Build a readable, DNS-safe ConfigMap name for one Grafana dashboard.
The chart test enforces the Kubernetes 63-character limit and uniqueness.
*/}}
{{- define "neutree.grafana.dashboardConfigMapName" -}}
{{- $filename := . -}}
{{- $slug := $filename | trimSuffix ".json" | lower | replace "_" "-" -}}
{{- if not (hasSuffix "-grafana-dashboard" $slug) -}}
{{- $slug = printf "%s-grafana-dashboard" ($slug | trimSuffix "-dashboard") -}}
{{- end -}}
{{- $slug -}}
{{- end -}}

{{/*
Reject the pre-NEU-818 dashboard mount instead of rendering a broken upgrade.
When Helm reuses old values, all native replacement volumes must be supplied.
*/}}
{{- define "neutree.grafana.validateDashboardMounts" -}}
{{- $legacyMountFound := false -}}
{{- range .Values.grafana.extraConfigmapMounts }}
{{- $configMap := default "" .configMap | toString -}}
{{- $mountPath := default "" .mountPath | toString -}}
{{- if or (eq (tpl $configMap $) "grafana-dashboards") (eq (tpl $mountPath $) "/var/lib/grafana/dashboards") -}}
{{- $legacyMountFound = true -}}
{{- end -}}
{{- end -}}
{{- if $legacyMountFound -}}
{{- fail "grafana.extraConfigmapMounts contains the removed Grafana dashboard mount; run helm upgrade with --reset-values or explicitly provide native dashboard volume values" -}}
{{- end -}}

{{- $dashboardVolumeFound := false -}}
{{- range .Values.grafana.extraContainerVolumes }}
{{- $name := default "" .name | toString -}}
{{- $sources := dig "projected" "sources" (list) . -}}
{{- if and (eq $name "grafana-dashboards") (gt (len $sources) 0) -}}
{{- $dashboardVolumeFound = true -}}
{{- end -}}
{{- end -}}
{{- if not $dashboardVolumeFound -}}
{{- fail "grafana native dashboard volume is missing; run helm upgrade with --reset-values or provide grafana.extraContainerVolumes and grafana.extraVolumeMounts" -}}
{{- end -}}

{{- $dashboardMountFound := false -}}
{{- range .Values.grafana.extraVolumeMounts }}
{{- $name := default "" .name | toString -}}
{{- $mountPath := default "" .mountPath | toString -}}
{{- if and (eq $name "grafana-dashboards") (eq $mountPath "/var/lib/grafana/dashboards") -}}
{{- $dashboardMountFound = true -}}
{{- end -}}
{{- end -}}
{{- if not $dashboardMountFound -}}
{{- fail "grafana native dashboard mount is missing; run helm upgrade with --reset-values or provide grafana.extraVolumeMounts" -}}
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

{{/*
Pod-level scheduling fields from a component values block that has
nodeSelector / affinity / tolerations. Renders nothing when all are empty.
*/}}
{{- define "neutree.podScheduling" -}}
{{- with .nodeSelector }}
nodeSelector:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .affinity }}
affinity:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- with .tolerations }}
tolerations:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}


{{/*
"true" when HA mode is on, empty otherwise. Tolerates values from releases
installed before the ha block existed.
*/}}
{{- define "neutree.ha.enabled" -}}
{{- if and .Values.ha .Values.ha.enabled -}}true{{- end -}}
{{- end -}}

{{/*
Replica count for a component that can run several replicas.
Usage: include "neutree.replicaCount" (dict "root" $ "values" .Values.api)
An explicit replicaCount wins (0 included); otherwise 3 in HA mode, else 1.
*/}}
{{- define "neutree.replicaCount" -}}
{{- if not (kindIs "invalid" .values.replicaCount) -}}
{{- .values.replicaCount -}}
{{- else if include "neutree.ha.enabled" .root -}}
3
{{- else -}}
1
{{- end -}}
{{- end -}}

{{/*
Pod scheduling for a component that can run several replicas. In HA mode an
empty affinity is replaced by a required one-pod-per-node anti-affinity on the
component's selector label; an explicit affinity is used as given.
Usage: include "neutree.ha.podScheduling" (dict "root" $ "values" .Values.api "component" "neutree-api")
*/}}
{{- define "neutree.ha.podScheduling" -}}
{{- $scheduling := dict "nodeSelector" .values.nodeSelector "affinity" .values.affinity "tolerations" .values.tolerations -}}
{{- if and (include "neutree.ha.enabled" .root) (empty .values.affinity) -}}
{{- $term := dict "labelSelector" (dict "matchLabels" (dict "app.kubernetes.io/component" .component)) "topologyKey" "kubernetes.io/hostname" -}}
{{- $_ := set $scheduling "affinity" (dict "podAntiAffinity" (dict "requiredDuringSchedulingIgnoredDuringExecution" (list $term))) -}}
{{- end -}}
{{- include "neutree.podScheduling" $scheduling -}}
{{- end -}}

{{/*
Rolling update strategy in HA mode. maxSurge stays 0 because the required
anti-affinity leaves no node for a surge pod when replicas equal nodes.
*/}}
{{- define "neutree.ha.strategy" -}}
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxSurge: 0
    maxUnavailable: 1
{{- end -}}

{{/*
Components that run several replicas in HA mode, keyed by values block, with
the component label their Deployment selects on.
*/}}
{{- define "neutree.ha.components" -}}
api: neutree-api
auth: neutree-auth
postgrest: neutree-postgrest
core: neutree-core
kong: {{ include "neutree.fullname" . }}-kong
vector: neutree-vector
{{- end -}}

{{/*
PostgreSQL runs as a CloudNativePG Cluster. Every component connects through
the read-write Service, which follows the primary across failovers.
*/}}
{{- define "neutree.db.clusterName" -}}
{{- printf "%s-pg" (include "neutree.fullname" .) -}}
{{- end -}}

{{- define "neutree.db.host" -}}
{{- printf "%s-rw" (include "neutree.db.clusterName" .) -}}
{{- end -}}

{{- define "neutree.db.port" -}}
5432
{{- end -}}

{{/*
libpq-style connection parameters (TCP keepalive) for clients that understand
them; empty when db.connectionParams is empty.
*/}}
{{- define "neutree.db.connectionParams" -}}
{{- .Values.db.connectionParams | default "" -}}
{{- end -}}

{{/*
Connection URL. Usage:
  include "neutree.db.url" (dict "root" $ "user" "u" "password" "p" "params" "sslmode=disable" "keepalive" true)
"params" is prepended to the shared keepalive parameters when "keepalive" is true.
*/}}
{{- define "neutree.db.url" -}}
{{- $params := list -}}
{{- with .params }}{{ $params = append $params . }}{{ end -}}
{{- if .keepalive }}{{ with include "neutree.db.connectionParams" .root }}{{ $params = append $params . }}{{ end }}{{ end -}}
{{- printf "postgres://%s:%s@%s:%s/%s" .user .password (include "neutree.db.host" .root) (include "neutree.db.port" .root) .root.Values.db.name -}}
{{- with $params }}?{{ join "&" . }}{{ end -}}
{{- end -}}

{{- define "neutree.db.superuserSecretName" -}}
{{- printf "%s-superuser" (include "neutree.db.clusterName" .) -}}
{{- end -}}

{{- define "neutree.db.migrationJobName" -}}
{{- printf "%s-pg-migration-%d" (include "neutree.fullname" .) (int .Release.Revision) -}}
{{- end -}}

{{/*
Init container that holds a pod until the database migration Job of this
release has finished, so nothing touches the new database before its data is
in place.
*/}}
{{- define "neutree.db.waitContainer" -}}
- name: wait-pg-migration
  image: {{ include "neutree.image" (dict "global" .Values.global "componentRegistry" .Values.k8sWaitFor.image.registry "repository" .Values.k8sWaitFor.image.repository "tag" .Values.k8sWaitFor.image.tag "defaultTag" .Values.k8sWaitFor.image.tag) }}
  imagePullPolicy: {{ .Values.k8sWaitFor.image.pullPolicy }}
  args:
    - job-wr
    - {{ include "neutree.db.migrationJobName" . }}
    - -n
    - {{ .Release.Namespace }}
{{- end -}}

{{/*
Init container for Deployments: holds the pod until the migration Job has
recorded that the database is in place. It asks the database rather than
waiting for the Job, whose name changes with every release revision and would
restart these pods on each helm upgrade.
*/}}
{{- define "neutree.db.waitReadyContainer" -}}
- name: wait-pg-ready
  image: {{ include "neutree.image" (dict "global" .Values.global "componentRegistry" .Values.db.image.registry "repository" .Values.db.image.repository "tag" .Values.db.image.tag "defaultTag" .Values.db.image.tag) }}
  imagePullPolicy: {{ .Values.db.image.pullPolicy }}
  command:
    - sh
    - -c
    - until psql -XtAq -h "$PGHOST" -d postgres -c 'select 1 from neutree_pg_migration limit 1' 2>/dev/null | grep -q 1; do echo waiting for the database migration; sleep 2; done
  env:
    - name: PGHOST
      value: {{ include "neutree.db.host" . }}
    - name: PGUSER
      value: postgres
    - name: PGPASSWORD
      valueFrom:
        secretKeyRef:
          name: {{ include "neutree.db.superuserSecretName" . }}
          key: password
    - name: PGCONNECT_TIMEOUT
      value: "3"
{{- end -}}

{{/*
KONG_PG_* environment shared by the Kong Deployment and its migration Jobs.
Kong's driver has no TCP keepalive option; its pg_timeout bounds every query
on a dead connection instead.
*/}}
{{- define "neutree.kong.pgEnv" -}}
- name: KONG_PG_DATABASE
  value: {{ .Values.db.name | quote }}
- name: KONG_PG_HOST
  value: {{ include "neutree.db.host" . | quote }}
- name: KONG_PG_PASSWORD
  value: "kong_admin_password"
- name: KONG_PG_PORT
  value: {{ include "neutree.db.port" . | quote }}
- name: KONG_PG_SCHEMA
  value: "kong"
- name: KONG_PG_USER
  value: "kong_admin"
{{- end -}}

{{/*
Fail early when the CloudNativePG CRDs are missing. Offline rendering (helm
template) has no cluster to ask, so db.operatorCheck can turn this off.
*/}}
{{- define "neutree.db.requireOperator" -}}
{{- if and .Values.db.operatorCheck (not (.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1")) -}}
{{- fail "PostgreSQL now runs on CloudNativePG, but the postgresql.cnpg.io/v1 API is not installed in this cluster. Install the CloudNativePG operator first (see deploy/chart/neutree/README.md, \"Prerequisites\"). For offline rendering with helm template, set db.operatorCheck=false." -}}
{{- end -}}
{{- end -}}

{{/*
Number of PostgreSQL instances: db.instances when set, else 3 in HA mode, else 1.
*/}}
{{- define "neutree.db.instances" -}}
{{- if not (kindIs "invalid" .Values.db.instances) -}}
{{- .Values.db.instances -}}
{{- else if include "neutree.ha.enabled" . -}}
3
{{- else -}}
1
{{- end -}}
{{- end -}}
