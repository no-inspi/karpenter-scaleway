{{/*
Standard Helm helpers for karpenter-scaleway.
*/}}

{{- define "karpenter-scaleway.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "karpenter-scaleway.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "karpenter-scaleway.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "karpenter-scaleway.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "karpenter-scaleway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "karpenter-scaleway.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "karpenter-scaleway.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "karpenter-scaleway.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "karpenter-scaleway.secretName" -}}
{{- if .Values.scaleway.existingSecret -}}
{{- .Values.scaleway.existingSecret -}}
{{- else -}}
{{- include "karpenter-scaleway.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "karpenter-scaleway.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
