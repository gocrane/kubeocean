{{/*
Expand the name of the chart.
*/}}
{{- define "kubeocean.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kubeocean.fullname" -}}
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
{{- define "kubeocean.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubeocean.labels" -}}
helm.sh/chart: {{ include "kubeocean.chart" . }}
{{ include "kubeocean.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubeocean.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubeocean.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Manager labels
*/}}
{{- define "kubeocean.manager.labels" -}}
{{ include "kubeocean.labels" . }}
app.kubernetes.io/component: manager
control-plane: kubeocean-manager
k8s-app: kubeocean-manager
{{- end }}

{{/*
Manager selector labels
*/}}
{{- define "kubeocean.manager.selectorLabels" -}}
{{ include "kubeocean.selectorLabels" . }}
app.kubernetes.io/component: manager
control-plane: kubeocean-manager
k8s-app: kubeocean-manager
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kubeocean.manager.serviceAccountName" -}}
{{- if .Values.manager.serviceAccount.create }}
{{- default (printf "%s-manager" (include "kubeocean.fullname" .)) .Values.manager.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.manager.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the manager cluster role
*/}}
{{- define "kubeocean.manager.clusterRoleName" -}}
{{- default (printf "%s-manager-role" (include "kubeocean.fullname" .)) .Values.manager.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the manager cluster role binding
*/}}
{{- define "kubeocean.manager.clusterRoleBindingName" -}}
{{- default (printf "%s-manager" (include "kubeocean.fullname" .)) .Values.manager.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Get the manager image name
*/}}
{{- define "kubeocean.manager.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.manager.image.registry -}}
{{- $repository := .Values.manager.image.repository -}}
{{- $tag := .Values.manager.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Create the name of the syncer template configmap
*/}}
{{- define "kubeocean.syncerTemplate.configMapName" -}}
{{- printf "%s-syncer-template" (include "kubeocean.fullname" .) -}}
{{- end -}}

{{/*
Syncer labels
*/}}
{{- define "kubeocean.syncer.labels" -}}
{{ include "kubeocean.labels" . }}
app.kubernetes.io/component: syncer
app.kubernetes.io/part-of: kubeocean
app.kubernetes.io/managed-by: kubeocean-manager
{{- end }}

{{/*
Syncer selector labels
*/}}
{{- define "kubeocean.syncer.selectorLabels" -}}
{{ include "kubeocean.selectorLabels" . }}
app.kubernetes.io/component: syncer
app.kubernetes.io/part-of: kubeocean
app.kubernetes.io/managed-by: kubeocean-manager
{{- end }}

{{/*
Create the name of the syncer service account to use
*/}}
{{- define "kubeocean.syncer.serviceAccountName" -}}
{{- if .Values.syncer.serviceAccount.create }}
{{- default (printf "%s-syncer" (include "kubeocean.fullname" .)) .Values.syncer.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.syncer.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the syncer cluster role
*/}}
{{- define "kubeocean.syncer.clusterRoleName" -}}
{{- default (printf "%s-syncer" (include "kubeocean.fullname" .)) .Values.syncer.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the syncer cluster role binding
*/}}
{{- define "kubeocean.syncer.clusterRoleBindingName" -}}
{{- default (printf "%s-syncer" (include "kubeocean.fullname" .)) .Values.syncer.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Get the syncer image name
*/}}
{{- define "kubeocean.syncer.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.syncer.image.registry -}}
{{- $repository := .Values.syncer.image.repository -}}
{{- $tag := .Values.syncer.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}

{{/*
Create the name of the proxier template configmap
*/}}
{{- define "kubeocean.proxierTemplate.configMapName" -}}
{{- printf "%s-proxier-template" (include "kubeocean.fullname" .) -}}
{{- end -}}

{{/*
Proxier labels
*/}}
{{- define "kubeocean.proxier.labels" -}}
{{ include "kubeocean.labels" . }}
app.kubernetes.io/component: proxier
app.kubernetes.io/part-of: kubeocean
app.kubernetes.io/managed-by: kubeocean-manager
{{- end }}

{{/*
Proxier selector labels
*/}}
{{- define "kubeocean.proxier.selectorLabels" -}}
{{ include "kubeocean.selectorLabels" . }}
app.kubernetes.io/component: proxier
app.kubernetes.io/part-of: kubeocean
app.kubernetes.io/managed-by: kubeocean-manager
{{- end }}

{{/*
Create the name of the proxier service account to use
*/}}
{{- define "kubeocean.proxier.serviceAccountName" -}}
{{- if .Values.proxier.serviceAccount.create }}
{{- default (printf "%s-proxier" (include "kubeocean.fullname" .)) .Values.proxier.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.proxier.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the proxier cluster role
*/}}
{{- define "kubeocean.proxier.clusterRoleName" -}}
{{- default (printf "%s-proxier" (include "kubeocean.fullname" .)) .Values.proxier.rbac.clusterRoleName }}
{{- end }}

{{/*
Create the name of the proxier cluster role binding
*/}}
{{- define "kubeocean.proxier.clusterRoleBindingName" -}}
{{- default (printf "%s-proxier" (include "kubeocean.fullname" .)) .Values.proxier.rbac.clusterRoleBindingName }}
{{- end }}

{{/*
Get the proxier image name
*/}}
{{- define "kubeocean.proxier.image" -}}
{{- $registry := .Values.global.imageRegistry | default .Values.proxier.image.registry -}}
{{- $repository := .Values.proxier.image.repository -}}
{{- $tag := .Values.proxier.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" $registry $repository $tag -}}
{{- end -}}