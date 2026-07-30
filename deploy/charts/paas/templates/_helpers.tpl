{{/*
PaaS core 资源全名（<release>-core）。
*/}}
{{- define "paas.fullname" -}}
{{ .Release.Name }}-core
{{- end -}}

{{/*
标准标签。
*/}}
{{- define "paas.labels" -}}
app.kubernetes.io/name: paas-core
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/component: control-plane
{{- end -}}

{{/*
core Pod 选择器标签。
*/}}
{{- define "paas.coreSelector" -}}
app.kubernetes.io/name: paas-core
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
