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

{{/*
The ConfigMap that holds the egress gateway's CA bundle. The bootstrap Job
writes it and the MutatingAdmissionPolicy mounts it, so both use this name.
*/}}
{{- define "kagent-addon.egressCABundleName" -}}
egress-ca-bundle
{{- end -}}

{{- define "kagent-addon.labels" -}}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: kagent-addon
{{- end -}}

{{/* Pod Security Admission labels for a namespace that runs Substrate workers. */}}
{{- define "kagent-addon.podSecurityLabels" -}}
pod-security.kubernetes.io/enforce: {{ .Values.podSecurity.level | quote }}
pod-security.kubernetes.io/audit: {{ .Values.podSecurity.level | quote }}
pod-security.kubernetes.io/warn: {{ .Values.podSecurity.level | quote }}
{{- end -}}

{{/*
The bucket-init Job's pod template. A Job's template cannot be patched, so the
Job's name carries a hash of this text: any change (image, bucket, security
context, resources) makes a new Job, and Helm deletes the old one.
*/}}
{{- define "kagent-addon.bucketInitTemplate" }}
  template:
    spec:
      restartPolicy: OnFailure
      serviceAccountName: {{ include "kagent-addon.rustfsName" . }}
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      {{- include "kagent-addon.imagePullSecrets" . | nindent 6 }}
      containers:
      - name: create-bucket
        image: {{ include "kagent-addon.image" (list .Values.rustfs.bucketInitImage .) }}
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop: [ALL]
        resources:
          {{- toYaml .Values.rustfs.bucketInit.resources | nindent 10 }}
        env:
        - name: AWS_ACCESS_KEY_ID
          valueFrom:
            secretKeyRef:
              name: {{ .Values.rustfs.credentialsSecret }}
              key: AWS_ACCESS_KEY_ID
        - name: AWS_SECRET_ACCESS_KEY
          valueFrom:
            secretKeyRef:
              name: {{ .Values.rustfs.credentialsSecret }}
              key: AWS_SECRET_ACCESS_KEY
        # The image runs as UID 10001 here, which has no home directory.
        - name: HOME
          value: /tmp
        - name: AWS_REGION
          value: us-east-1
        - name: AWS_ENDPOINT_URL
          value: http://{{ include "kagent-addon.rustfsName" . }}.{{ .Release.Namespace }}.svc:9000
        command:
        - /bin/sh
        - -c
        - |
          set -e
          for i in $(seq 1 60); do
            if aws s3api head-bucket --bucket {{ .Values.rustfs.bucket }} 2>/dev/null; then
              echo "bucket {{ .Values.rustfs.bucket }} already exists"
              exit 0
            fi
            if aws s3api create-bucket --bucket {{ .Values.rustfs.bucket }} 2>/dev/null; then
              echo "bucket {{ .Values.rustfs.bucket }} created"
              exit 0
            fi
            echo "waiting for rustfs to become available... ($i/60)"
            sleep 2
          done
          echo "timed out waiting for rustfs"
          exit 1
{{- end -}}

{{- define "kagent-addon.bucketInitName" -}}
{{- printf "%s-bucket-init-%s" (include "kagent-addon.rustfsName" .) (include "kagent-addon.bucketInitTemplate" . | sha256sum | trunc 10) -}}
{{- end -}}
