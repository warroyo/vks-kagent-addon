# Upstream workarounds

What this repo carries for problems in kagent's Substrate fork chart v1.0.0
(`kagent-dev/substrate`), kagent v1.0.1 and `agent-substrate/substrate`. Issue
and PR states are as of 2026-11-13. Each section says when the workaround can go.

## Constraint: the release is named `kagent`

The Substrate subchart prefixes every object with the release name and puts
objects in the release namespace. It has no name or namespace overrides. So the
release must be named `kagent`, and values use the prefixed names, such as
`kagent-api.ate-system.svc`. The ACD derives the prefixed endpoints and
identities (`chart/addon-config-definition.yaml`), and `chart/kagent/values.yaml`
repeats them for `helm template`. See
[Architecture](architecture.md#names-and-namespaces). This is the cause of the
atelet workaround below.

## Credential provider NetworkPolicy blocks the egress gateway

Upstream problem: the subchart's NetworkPolicy `kagent-k8s-credential-provider`
admits ingress only from pods labelled `app: <release>-atenet-egress`. The egress
gateway pods are labelled `app: atenet-egress`. The gateway cannot fetch
credentials, and requests fail with 503 and `FetchSecret timed out`.

Here: an extra NetworkPolicy, `kagent-k8s-credential-provider-from-egress`, in
`chart/kagent/templates/networkpolicies.yaml`. NetworkPolicies are additive, so
it only adds the missing source.

Upstream:

- kagent-dev/substrate#179: open, filed by us
- kagent-dev/substrate#171 and #174: open, a `networkPolicies.enabled` toggle

Remove when #179 is fixed in the subchart version this chart depends on.

## The egress gateway cannot trust a private CA

Upstream problem: the gateway verifies upstream TLS only against the CA file in
its image. No chart value adds a CA. The kagent ModelConfig `tls` field has only
`disableVerify`, which covers the agent-to-gateway hop, not the gateway to the
model endpoint.

Here: the ConfigMap `egress-ca-bundle` (Mozilla's roots in
`chart/kagent/files/ca-roots.pem`, plus `egressUpstreamTrust.caBundle`) in
`chart/kagent/templates/egress-upstream-trust.yaml`, and a
MutatingAdmissionPolicy and binding emitted by the ACD
(`chart/addon-config-definition.yaml`) that mount it over
`/etc/ssl/certs/ca-certificates.crt`. After a CA change, delete the egress pods.
See [the FAQ](faq.md#how-do-i-trust-a-private-ca-for-egress) and
[Decisions](decisions.md#the-egress-gateways-extra-ca-is-built-declaratively).

Upstream:

- kagent-dev/substrate#100: open
- kagent-dev/substrate#101: open, a PR adding `atenetEgress.upstreamTrust.caBundle`
- agent-substrate/substrate#2684: open
- agent-substrate/substrate#3648: open

Remove when #101 ships: set `substrate.atenetEgress.upstreamTrust.caBundle`
instead, and drop the ConfigMap, the ACD's policy outputs and
`scripts/sync-ca-roots.sh`.

## atelet addresses the unprefixed API

Upstream problem: atelet's `--ateapi-address` and `--ateapi-server-name` default
to `api.ate-system.svc`. That Service does not exist in a prefixed release, and
the chart does not pass the flags.

Here: `substrate.atelet.extraArgs` in `chart/kagent/values.yaml`, derived by the
ACD.

Upstream: no issue filed yet.

Remove when the chart passes the flags from the release name.

## RustFS keys are plain values

Upstream problem: the chart takes RustFS keys only as values. VKS helm values
are plain text in the AddonConfig.

Here: `substrate.rustfs.enabled=false`, our own RustFS in
`chart/kagent/templates/rustfs.yaml` reading a Secret that the bootstrap Job
generates, and `substrate.ateApi.snapshotLocation` set explicitly, because the
chart no longer derives it without its RustFS.

Upstream:

- kagent-dev/substrate#169: open PR, `rustfs.existingSecret`

Remove when #169 ships: use the chart's RustFS with the existing Secret.

## CA Secrets are created outside the chart

Upstream problem: Substrate needs its CA Secrets before install (the pod
certificate pools, `actor-id`, the egress MITM CA). Upstream creates them in
`kagent install` and `ate-setup`, not in the chart.

Here: the bootstrap Job in `bootstrap/` and `chart/kagent/templates/bootstrap.yaml`,
adapted from kagent's `go/core/pkg/install/substrate_init.go`.

Upstream: agent-substrate/substrate#3175, open, install bootstrap.

Remove when the chart creates these Secrets itself.
