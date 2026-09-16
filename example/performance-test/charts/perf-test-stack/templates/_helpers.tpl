{{/*
Expand the name of the chart.
*/}}
{{- define "fluent-bit-plugin.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "fluent-bit-plugin.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "fluent-bit-plugin.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Per-node host paths backing the queue (bbolt) and chunks volumes.
Both derive from fluentbit.hostPath so there is a single base to configure.
*/}}
{{- define "fluent-bit-plugin.hostQueuePath" -}}
{{- printf "%s/bbolt" .Values.fluentbit.hostPath }}
{{- end }}

{{- define "fluent-bit-plugin.hostChunksPath" -}}
{{- printf "%s/chunks" .Values.fluentbit.hostPath }}
{{- end }}
