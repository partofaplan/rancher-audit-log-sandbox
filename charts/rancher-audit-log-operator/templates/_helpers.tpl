{{/* Chart name / fullname */}}
{{- define "operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "operator.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s" (include "operator.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "operator.labels" -}}
app.kubernetes.io/name: {{ include "operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end -}}

{{- define "operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "operator.serviceAccountName" -}}
{{ include "operator.fullname" . }}-controller-manager
{{- end -}}

{{/* Full operator image ref, with optional registry prefix */}}
{{- define "operator.image" -}}
{{- if .Values.image.registry -}}
{{ .Values.image.registry }}/{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- else -}}
{{ .Values.image.repository }}:{{ .Values.image.tag }}
{{- end -}}
{{- end -}}

{{/* Full Filebeat image ref:
       1. filebeat.image set        -> use it verbatim
       2. image.registry set        -> <registry>/<filebeat.repository>:<tag>  (air-gap mirror)
       3. otherwise                 -> docker.elastic.co/<filebeat.repository>:<tag>  (public default) */}}
{{- define "operator.filebeatImage" -}}
{{- if .Values.filebeat.image -}}
{{ .Values.filebeat.image }}
{{- else if .Values.image.registry -}}
{{ .Values.image.registry }}/{{ .Values.filebeat.repository }}:{{ .Values.filebeat.tag }}
{{- else -}}
docker.elastic.co/{{ .Values.filebeat.repository }}:{{ .Values.filebeat.tag }}
{{- end -}}
{{- end -}}

{{/* Name of the basic-auth secret (existing, or one this chart creates) */}}
{{- define "operator.authSecretName" -}}
{{- if .Values.elasticsearch.auth.existingSecret -}}
{{ .Values.elasticsearch.auth.existingSecret }}
{{- else -}}
{{ include "operator.fullname" . }}-es-auth
{{- end -}}
{{- end -}}

{{/* Name of the CA secret (existing, or one this chart creates) */}}
{{- define "operator.caSecretName" -}}
{{- if .Values.elasticsearch.tls.existingCASecret -}}
{{ .Values.elasticsearch.tls.existingCASecret }}
{{- else -}}
{{ include "operator.fullname" . }}-es-ca
{{- end -}}
{{- end -}}
