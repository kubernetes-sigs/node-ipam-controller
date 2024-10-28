# Copyright 2023 The Kubernetes Authors.
# 
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
# 
#     http://www.apache.org/licenses/LICENSE-2.0
# 
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

{{/*
Expand the name of the chart.
*/}}
{{- define "node-ipam-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "node-ipam-controller.fullname" -}}
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
{{- define "node-ipam-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "node-ipam-controller.labels" -}}
helm.sh/chart: {{ include "node-ipam-controller.chart" . }}
{{ include "node-ipam-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "node-ipam-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "node-ipam-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "node-ipam-controller.serviceAccountName" -}}
{{- default (include "node-ipam-controller.fullname" .) .Values.serviceAccount.name }}
{{- end }}

{{/*
Leader Election
*/}}
{{- define "node-ipam-controller.leaderElection"}}
{{- if .Values.leaderElection.leaseDuration }}
- --leader-elect-lease-duration={{ .Values.leaderElection.leaseDuration }}
{{- end }}
{{- if .Values.leaderElection.renewDeadline }}
- --leader-elect-renew-deadline={{ .Values.leaderElection.renewDeadline }}
{{- end }}
{{- if .Values.leaderElection.retryPeriod }}
- --leader-elect-retry-period={{ .Values.leaderElection.retryPeriod }}
{{- end }}
{{- if .Values.leaderElection.resourceLock }}
- --leader-elect-resource-lock={{ .Values.leaderElection.resourceLock }}
{{- end }}
{{- if .Values.leaderElection.resourceName }}
- --leader-elect-resource-name={{ .Values.leaderElection.resourceName }}
{{- end }}
{{- end }}