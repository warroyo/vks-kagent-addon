# Decisions

One entry per decision that shapes the chart as it is now.

## One umbrella chart, release `kagent`

Date: 2026-11-12.

Context: kagent's own install uses three releases (`substrate`,
`substrate-podcert`, `kagent`) in three namespaces. The Substrate charts have no
name or namespace overrides, and a Helm parent chart cannot compute values for
its subcharts. On VKS the release name is the addon name.

Decision: one addon, `kagent`, as an umbrella chart. The release is `kagent`,
in `ate-system` (the ACD default), with kagent in `kagent`. The ACD derives the
values that must agree across subcharts. The CRDs of `substrate-crds` and
`kagent-crds` are copied into `crds/`, not added as dependencies.

Alternatives rejected:

- Three addons, one per kagent release. They would need to agree on names and
  identities by hand, and ordering across addons is not guaranteed.
- Inner Flux HelmReleases inside the addon. They hide the install from the
  addon manager and add a second reconciler.
- Our own Substrate chart. kagent publishes charts, and a prefixed release
  renders consistent names, SPIFFE IDs and issuer.
- `substrate-crds` and `kagent-crds` as subcharts. Their CRDs sit in
  `templates/`, so resources in the same release would apply before the CRDs
  exist.

## kagent's Substrate fork, v1.0.0

Date: 2026-11-12.

Context: upstream Substrate does not ship Helm charts. kagent publishes charts
for its fork and tests kagent v1.0.1 against fork v1.0.0.

Decision: kagent v1.0.1 on kagent's Substrate fork v1.0.0, the tested pairing.
The API is the same as upstream's. Revisit when upstream ships charts (epic
#3170).

Alternatives rejected: upstream Substrate manifests, which hardcode
`ate-system` and `podcertificate-controller-system` and would need our own
chart.

## Where the pieces run

Date: 2026-11-12.

Context: the umbrella chart differs from kagent's own layout.

Decision:

- Substrate names carry the `kagent-` prefix. The chart wires Substrate's side,
  and the ACD sets kagent's side.
- The pod certificate controller runs in `ate-system`. It has no ports and
  reads its CA pools from its own namespace, so the default-deny ingress policy
  does not affect it. `postgresClientServiceAccount` is `kagent-ate-api-server`.
- A bootstrap Job of our own creates the CA pools. kagent's `PrepareSubstrate`
  hardcodes `podcertificate-controller-system`, so the Job is a copy of
  `substrate_init.go` with the namespace as a parameter.

Alternatives rejected: calling kagent's `PrepareSubstrate` unchanged, which
needs the hardcoded namespace.

## One shared PostgreSQL

Date: 2026-11-12.

Context: kagent and Substrate both need PostgreSQL. Upstream's manifest does not
create the roles in the pod; the `ate-setup` CLI does.

Decision: one PostgreSQL in the `kagent` namespace holds kagent's and
Substrate's schemas and roles, as `kagent install` deploys it. Pod certificates
(signer `postgres.podcert.ate.dev`) authenticate clients, with no passwords.
`postgres:18-alpine` is pinned by the digest kagent uses. A native sidecar,
`db-setup`, creates the `kagent` database if it is missing and runs both
`setup.sql` scripts.

Alternatives rejected:

- One instance per product. It matches each product's layout, but costs a
  second database to run.
- A post-install hook for the setup scripts. Helm runs post-install hooks only
  after everything is Ready, which deadlocks with helm-controller's wait.

## Cluster label

Date: 2026-11-12.

Context: an AddonInstall selects clusters by label.

Decision: `addons.kubernetes.vmware.com/kagent: enabled`, the same shape as
other VKS addon labels.

## RustFS without plain-text credentials

Date: 2026-11-12.

Context: the `substrate` chart takes RustFS keys only as values (default
`rustfsadmin`), and VKS helm values are plain text in the AddonConfig.

Decision: the subchart's RustFS is off. This chart renders its own, reading a
Secret the bootstrap Job generates with random keys. Every consumer of the keys
takes a Secret.

Alternatives rejected: the chart default, which puts known keys in values.

## The bootstrap Job writes only to the release namespace

Date: 2026-11-12.

Context: helm-controller creates the release namespace before the first hook. A
Job that created namespaces could leave one owned by the wrong Helm release.

Decision: the Job creates no namespace. It writes Secrets only in the release
namespace, through a Role, and sets the Pod Security labels on that namespace,
because VKS enforces `restricted` and atelet needs `privileged`. The label
change uses a ClusterRole limited by `resourceNames` to that one namespace. The
`kagent` namespace is a plain chart template.

Alternatives rejected: a Namespace template for the release namespace. Helm does
not own that namespace.

## The SPIFFE trust domain stays `cluster.local`

Date: 2026-11-12.

Context: Substrate's charts hardcode `spiffe://cluster.local/...` in the flags
that check identities. A cluster can have another DNS domain.

Decision: the trust domain stays `cluster.local`. Only `global.clusterDomain`
follows the Cluster resource's `serviceDomain`.

Alternatives rejected: following the DNS domain in the trust domain, which
breaks the identity checks.

## The egress gateway's extra CA is declarative

Date: 2026-11-13.

Context: the gateway verifies upstream TLS only against its image's
`ca-certificates.crt`, so a model endpoint with a private CA fails.

Decision: the chart ships Mozilla's public roots
(`chart/kagent/files/ca-roots.pem`, refreshed by `scripts/sync-ca-roots.sh`) and
always renders them, plus `egressUpstreamTrust.caBundle` when set, into the
ConfigMap `egress-ca-bundle`. A MutatingAdmissionPolicy and binding that mount it
over the gateway's bundle come from the ACD as cluster-scoped outputs. The addon
manager applies them before the HelmRelease runs and removes them with the
addon. Because the ACD always emits the policy, the ConfigMap is always
rendered, or the gateway pods would hang in `ContainerCreating`. Without a
private CA the gateway trusts the chart's roots instead of its image's.

Cost: the pods read the bundle only at start, so after changing the value run
`kubectl -n ate-system delete pod -l app=atenet-egress`. The shipped roots age
until the chart is released again. All of it goes when kagent-dev/substrate#101
ships. See [Upstream workarounds](upstream-workarounds.md).

Alternatives rejected:

- The policy as Helm hooks in the chart. Hooks are imperative and untracked:
  Helm leaves them behind on uninstall.
- A Job that builds the bundle and deletes pods. Imperative, and it does not run
  again on a value change.
- Restarting the Deployment. That edits its pod template, which Flux drift
  detection on the HelmRelease may revert, rolling the pods again.
