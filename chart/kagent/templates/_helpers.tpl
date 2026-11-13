{{/*
The namespace kagent runs in. The kagent subchart and its kagent-tools subchart
both read it from namespaceOverride, so this chart reads the same value.
*/}}
{{- define "kagent-addon.kagentNamespace" -}}
{{- .Values.kagent.namespaceOverride | default "kagent" -}}
{{- end -}}

{{/* The name of the shared PostgreSQL and its Service. */}}
{{- define "kagent-addon.postgresName" -}}
kagent-postgresql
{{- end -}}

{{/* The RustFS object names, prefixed with the release like the substrate chart's. */}}
{{- define "kagent-addon.rustfsName" -}}
{{- printf "%s-rustfs" .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Rewrites a full image reference onto global.imageRegistry, the way the
substrate chart's substrate.thirdPartyImage does: the first path segment is a
registry only when it contains "." or ":"; otherwise the reference is
docker.io-implied and the mirror is prefixed. The repository path is kept.

Usage: {{ include "kagent-addon.image" (list "postgres:18-alpine" .) }}
*/}}
{{- define "kagent-addon.image" -}}
{{- $ref := index . 0 -}}
{{- $ctx := index . 1 -}}
{{- $mirror := ($ctx.Values.global).imageRegistry -}}
{{- if not $mirror -}}
{{- $ref -}}
{{- else -}}
{{- $parts := splitList "/" $ref -}}
{{- $first := first $parts -}}
{{- if and (gt (len $parts) 1) (or (contains "." $first) (contains ":" $first)) -}}
{{- printf "%s/%s" $mirror (join "/" (rest $parts)) -}}
{{- else -}}
{{- printf "%s/%s" $mirror $ref -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* The bootstrap image: registry (or the mirror) / repository : tag. */}}
{{- define "kagent-addon.bootstrapImage" -}}
{{- $image := .Values.bootstrap.image -}}
{{- printf "%s/%s:%s" (default $image.registry ($.Values.global).imageRegistry) $image.repository ($image.tag | default .Chart.AppVersion) -}}
{{- end -}}

{{/* imagePullSecrets for a pod spec, or nothing when the list is empty. */}}
{{- define "kagent-addon.imagePullSecrets" -}}
{{- with ($.Values.global).imagePullSecrets }}
imagePullSecrets:
{{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}

{{- define "kagent-addon.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: kagent-addon
{{- end -}}
