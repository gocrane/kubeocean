{{/*
Expand the name of the chart.
*/}}
{{- define "tapestry.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "tapestry.fullname" -}}
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
{{- define "tapestry.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "tapestry.labels" -}}
helm.sh/chart: {{ include "tapestry.chart" . }}
{{ include "tapestry.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "tapestry.selectorLabels" -}}
app.kubernetes.io/name: {{ include "tapestry.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Manager labels
*/}}
{{- define "tapestry.manager.labels" -}}
{{ include "tapestry.labels" . }}
app.kubernetes.io/component: manager
control-plane: tapestry-manager
k8s-app: tapestry-manager
{{- end }}

{{/*
Manager selector labels
*/}}
{{- define "tapestry.manager.selectorLabels" -}}
{{ include "tapestry.selectorLabels" . }}
app.kubernetes.io/component: manager
control-plane: tapestry-manager
k8s-app: tapestry-manager
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "tapestry.manager.serviceAccountName" -}}
{{- if .Values.manager.serviceAccount.create }}
{{- default (printf "%s-manager" (include "tapestry.fullname" .)) .Values.manager.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.manager.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the manager cluster role
*/}}
{{- define "tapestry.manager.clusterRoleName" -}}
{{- default (printf "%s-manager-role" (include "tapestry.fullname" .)) .Values.manager.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the manager cluster role binding
*/}}
{{- define "tapestry.manager.clusterRoleBindingName" -}}
{{- default (printf "%s-manager" (include "tapestry.fullname" .)) .Values.manager.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Get the manager image name
*/}}
{{- define "tapestry.manager.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.manager.image.registry -}}
{{- $repository := .Values.manager.image.repository -}}
{{- $tag := .Values.manager.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Create the name of the syncer template configmap
*/}}
{{- define "tapestry.syncerTemplate.configMapName" -}}
{{- printf "%s-syncer-template" (include "tapestry.fullname" .) -}}
{{- end -}}

{{/*
Syncer labels
*/}}
{{- define "tapestry.syncer.labels" -}}
{{ include "tapestry.labels" . }}
app.kubernetes.io/component: syncer
app.kubernetes.io/part-of: tapestry
app.kubernetes.io/managed-by: tapestry-manager
{{- end }}

{{/*
Syncer selector labels
*/}}
{{- define "tapestry.syncer.selectorLabels" -}}
{{ include "tapestry.selectorLabels" . }}
app.kubernetes.io/component: syncer
app.kubernetes.io/part-of: tapestry
app.kubernetes.io/managed-by: tapestry-manager
{{- end }}

{{/*
Create the name of the syncer service account to use
*/}}
{{- define "tapestry.syncer.serviceAccountName" -}}
{{- if .Values.syncer.serviceAccount.create }}
{{- default (printf "%s-syncer" (include "tapestry.fullname" .)) .Values.syncer.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.syncer.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the syncer cluster role
*/}}
{{- define "tapestry.syncer.clusterRoleName" -}}
{{- default (printf "%s-syncer" (include "tapestry.fullname" .)) .Values.syncer.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the syncer cluster role binding
*/}}
{{- define "tapestry.syncer.clusterRoleBindingName" -}}
{{- default (printf "%s-syncer" (include "tapestry.fullname" .)) .Values.syncer.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Get the syncer image name
*/}}
{{- define "tapestry.syncer.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.syncer.image.registry -}}
{{- $repository := .Values.syncer.image.repository -}}
{{- $tag := .Values.syncer.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}
