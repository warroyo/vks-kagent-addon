# Decisions

One entry per decision. Dates are the commit dates of the change.

## One umbrella chart, release `kagent`

Date: 2026-11-12.

Context: kagent's own install uses three releases (`substrate`,
`substrate-podcert`, `kagent`) in three namespaces. The Substrate charts have no
name or namespace overrides, and a Helm parent chart cannot compute values for
its subcharts. On VKS the release name is the addon name.

Decision: ship one addon, `kagent`, as an umbrella chart. The release is
`kagent`, in `ate-system` (the ACD default), with kagent in `kagent`. The ACD
derives the values that must agree across subcharts. The CRDs of
`substrate-crds` and `kagent-crds` are copied into `crds/`, not added as
dependencies.

Alternatives rejected:

- Three addons, one per kagent release. They would need to agree on names and
  identities by hand, and ordering across addons is not guaranteed.
- Inner Flux HelmReleases inside the addon. They hide the install from the
  addon manager and add a second reconciler.
- Writing our own Substrate chart. kagent publishes charts, and a prefixed
  release is supported: `helm template kagent ./substrate -n ate-system`
  renders consistent names, SPIFFE IDs and issuer.
- `substrate-crds` and `kagent-crds` as subcharts. Their CRDs sit in
  `templates/`, so the resources in the same release would apply before the
  CRDs exist.

## Four divergences from kagent's layout

Date: 2026-11-12.

Context: the umbrella chart differs from kagent's tested layout in four places.

Decision: handle each one.

1. Prefixed Substrate names. The chart wires its own side, and the ACD sets
   kagent's side.
2. The pod certificate controller runs in `ate-system`. It has no ports and
   reads its CA pools from its own namespace, so the default-deny ingress policy
   does not affect it. `postgresClientServiceAccount` is `kagent-ate-api-server`.
3. A bootstrap Job of our own. kagent's `PrepareSubstrate` hardcodes
   `podcertificate-controller-system` for the CA pools, so the Job is a copy of
   `substrate_init.go` with the namespace as a parameter.
4. One shared PostgreSQL (see the next entry).

Alternatives rejected: calling kagent's `PrepareSubstrate` unchanged, which
needs the hardcoded namespace.

## One shared PostgreSQL

Date: 2026-11-12.

Context: the first design had two instances, one per product. Upstream's
manifest does not bootstrap roles in the pod. The `ate-setup` CLI does that.

Decision: one PostgreSQL in the `kagent` namespace holds kagent's and
Substrate's schemas and roles, as kagent's own `kagent install` deploys it. Pod
certificates (signer `postgres.podcert.ate.dev`) authenticate clients, with no
passwords. `postgres:18-alpine` is pinned by the digest kagent uses. A native
sidecar, `db-setup`, runs both `setup.sql` scripts.

Alternatives rejected:

- Two instances. They match each product's own layout, but cost a second
  database to run.
- A post-install hook for the setup scripts. Helm runs post-install hooks only
  after everything is Ready, which would deadlock with helm-controller's wait.

## Cluster label

Date: 2026-11-12.

Context: an AddonInstall selects clusters by label.

Decision: `addons.kubernetes.vmware.com/kagent: enabled`, the same shape as
other VKS addon labels.

Alternatives rejected: `kagent.vmware.com/enabled`, which is not in the
`addons.kubernetes.vmware.com` family.

## RustFS without plain-text credentials

Date: 2026-11-12.

Context: the `substrate` chart takes RustFS keys only as values (default
`rustfsadmin`), and VKS helm values are plain text in the AddonConfig.

Decision: switch the subchart's RustFS off. This chart renders its own, reading
a Secret the bootstrap Job generates with random keys. Every consumer of the
keys can take a Secret.

Alternatives rejected: the chart default, which puts known keys in values.

## The bootstrap Job writes only to the release namespace

Date: 2026-11-12.

Context: the helm-controller creates the release namespace before the first
hook. A Job that created namespaces could leave one owned by the wrong Helm
release.

Decision: the Job creates no namespace and writes Secrets and ConfigMaps only in
the release namespace, through a Role. The `kagent` namespace is a plain chart
template. Later the same day the Job also started setting the Pod Security labels
on the release namespace, because VKS enforces `restricted` and atelet needs
`privileged`. That uses a ClusterRole limited by `resourceNames` to that one
namespace.

Alternatives rejected: a Namespace template for the release namespace. Helm does
not own that namespace.

## The SPIFFE trust domain stays `cluster.local`

Date: 2026-11-12.

Context: Substrate's charts hardcode `spiffe://cluster.local/...` in the flags
that check identities. A cluster can have another DNS domain.

Decision: the trust domain stays `cluster.local`. Only `global.clusterDomain`
follows the Cluster resource's `serviceDomain`.

Alternatives rejected: following the DNS domain in the trust domain, which would
break the identity checks.

## kagent's Substrate fork, v1.0.0

Date: 2026-11-12.

Context: upstream Substrate does not yet ship Helm charts. kagent publishes
charts for its fork and tests v1.0.1 against fork v1.0.0.

Decision: use kagent v1.0.1 on kagent's Substrate fork v1.0.0, the tested
pairing. Revisit when upstream ships charts (epic #3170). The API is identical.

Alternatives rejected: upstream Substrate manifests, which hardcode `ate-system`
and `podcertificate-controller-system` and would need our own chart.

## The egress gateway's extra CA is built declaratively

Date: 2026-11-13.

Context: the gateway verifies upstream TLS only against its image's
`ca-certificates.crt`, so a model endpoint with a private CA fails.

Decision: the chart ships Mozilla's public roots
(`chart/kagent/files/ca-roots.pem`, refreshed by `scripts/sync-ca-roots.sh`) and
always renders them, plus `egressUpstreamTrust.caBundle` when set, into the
ConfigMap `egress-ca-bundle`. A MutatingAdmissionPolicy and binding that mount it
over the gateway's bundle come from the ACD as cluster-scoped outputs, not from
the chart as Helm hooks. Hooks are imperative and untracked (Helm leaves them
behind on uninstall), while the addon manager applies and removes ACD outputs
with the addon, before the HelmRelease runs. Because the ACD always emits the
policy, the ConfigMap is always rendered, or the gateway pods would hang in
`ContainerCreating`. Without a private CA the gateway trusts the chart's roots
instead of its image's.

The cost is manual: the pods read the bundle only at start, so after changing the
value run `kubectl -n ate-system delete pod -l app=atenet-egress`. The
Deployment recreates the pods and the policy mounts the new bundle. The shipped
roots also go stale until the chart is released again. All of it goes when
kagent-dev/substrate#101 ships (tracked in agent-substrate/substrate#2684).

Alternatives rejected:

- A Job that wrote the ConfigMap and deleted pods. It was imperative and
  untracked, so it was dropped.
- Restarting the Deployment. That edits its pod template, which Flux drift
  detection on the HelmRelease may revert, rolling the pods again.
- Building the bundle in the bootstrap Job (tried first, 2026-11-13). The Job
  does not run again on a value change.
