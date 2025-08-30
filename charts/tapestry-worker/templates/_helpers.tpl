{{/*
Expand the name of the chart.
*/}}
{{- define "tapestry-worker.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "tapestry-worker.fullname" -}}
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
{{- define "tapestry-worker.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "tapestry-worker.labels" -}}
helm.sh/chart: {{ include "tapestry-worker.chart" . }}
{{ include "tapestry-worker.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "tapestry-worker.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tapestry-worker.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the namespace
*/}}
{{- define "tapestry-worker.namespaceName" -}}
{{- default (printf "%s-system" (include "tapestry-worker.fullname" .)) .Values.namespace.name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "tapestry-worker.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-syncer" (include "tapestry-worker.fullname" .)) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the cluster role
*/}}
{{- define "tapestry-worker.clusterRoleName" -}}
{{- default (printf "%s-syncer" (include "tapestry-worker.fullname" .)) .Values.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the cluster role binding
*/}}
{{- define "tapestry-worker.clusterRoleBindingName" -}}
{{- default (printf "%s-syncer" (include "tapestry-worker.fullname" .)) .Values.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Create the name of the role
*/}}
{{- define "tapestry-worker.roleName" -}}
{{- default (printf "%s-syncer" (include "tapestry-worker.fullname" .)) .Values.rbac.roleName }}
{{- end }}

{{/*
Create the name of the role binding
*/}}
{{- define "tapestry-worker.roleBindingName" -}}
{{- default (printf "%s-syncer" (include "tapestry-worker.fullname" .)) .Values.rbac.roleBindingName }}
{{- end }}


