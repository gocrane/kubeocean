{{/*
Expand the name of the chart.
*/}}
{{- define "kubeocean-worker.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kubeocean-worker.fullname" -}}
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
{{- define "kubeocean-worker.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubeocean-worker.labels" -}}
helm.sh/chart: {{ include "kubeocean-worker.chart" . }}
{{ include "kubeocean-worker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubeocean-worker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubeocean-worker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the namespace
*/}}
{{- define "kubeocean-worker.namespaceName" -}}
{{- default (printf "%s-system" (include "kubeocean-worker.fullname" .)) .Values.namespace.name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubeocean-worker.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-syncer" (include "kubeocean-worker.fullname" .)) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the cluster role
*/}}
{{- define "kubeocean-worker.clusterRoleName" -}}
{{- default (printf "%s-syncer" (include "kubeocean-worker.fullname" .)) .Values.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the cluster role binding
*/}}
{{- define "kubeocean-worker.clusterRoleBindingName" -}}
{{- default (printf "%s-syncer" (include "kubeocean-worker.fullname" .)) .Values.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Create the name of the role
*/}}
{{- define "kubeocean-worker.roleName" -}}
{{- default (printf "%s-syncer" (include "kubeocean-worker.fullname" .)) .Values.rbac.roleName }}
{{- end }}

{{/*
Create the name of the role binding
*/}}
{{- define "kubeocean-worker.roleBindingName" -}}
{{- default (printf "%s-syncer" (include "kubeocean-worker.fullname" .)) .Values.rbac.roleBindingName }}
{{- end }}


