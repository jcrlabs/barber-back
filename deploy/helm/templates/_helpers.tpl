{{- define "barber-back.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "barber-back.fullname" -}}
{{- include "barber-back.name" . }}
{{- end }}

{{- define "barber-back.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
app.kubernetes.io/name: {{ include "barber-back.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/part-of: barber
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{- define "barber-back.selectorLabels" -}}
app.kubernetes.io/name: {{ include "barber-back.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
