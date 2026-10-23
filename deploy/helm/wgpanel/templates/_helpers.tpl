{{/* 通用名称工具：fullname / chart / labels / selectorLabels */}}

{{- define "wgpanel.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "wgpanel.fullname" -}}
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

{{- define "wgpanel.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "wgpanel.labels" -}}
helm.sh/chart: {{ include "wgpanel.chart" . }}
{{ include "wgpanel.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: wgpanel
{{- end -}}

{{- define "wgpanel.selectorLabels" -}}
app.kubernetes.io/name: {{ include "wgpanel.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "wgpanel.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "wgpanel.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* 镜像仓库引用 */}}
{{- define "wgpanel.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/*
持久卷名称。未创建 PVC 时返回空串，调用方需判断。
*/}}
{{- define "wgpanel.pvcName" -}}
{{- if and .Values.persistence.enabled (not .Values.persistence.existingClaim) -}}
{{- printf "%s-data" (include "wgpanel.fullname" .) -}}
{{- else -}}
{{- .Values.persistence.existingClaim -}}
{{- end -}}
{{- end -}}

{{/*
管理员密码。优先级：
  admin.existingSecret > admin.password > 空（面板随机生成并打印到日志）
*/}}
{{- define "wgpanel.adminPasswordEnv" -}}
{{- if .Values.admin.existingSecret -}}
- name: WGP_ADMIN_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.admin.existingSecret }}
      key: password
{{- else if .Values.admin.password -}}
- name: WGP_ADMIN_PASSWORD
  value: {{ .Values.admin.password | quote }}
{{- end -}}
{{- end -}}