# Architecture

## Components

One helm release, `kagent`, installed by helm-controller in the cluster. The
release namespace is `ate-system`. The addon gives it the name and namespace,
and the chart depends on both (see [Names](#names-and-namespaces)).

| Piece | Namespace | Source |
|---|---|---|
| Substrate: API, controller, IdP, router, egress gateway, credential provider, atelet, gVisor `SandboxConfig` | `ate-system` | subchart `substrate` 1.0.0 (kagent's fork) |
| Pod certificate controller | `ate-system` | subchart `substrate-podcert` 1.0.0 |
| RustFS (snapshot storage) | `ate-system` | this chart's template |
| Bootstrap Job (pre-install and pre-upgrade hook) | `ate-system` | this chart's template and image |
| kagent controller, UI, tools | `kagent` | subchart `kagent` 1.0.1, `namespaceOverride: kagent` |
| PostgreSQL, shared by kagent and Substrate | `kagent` | this chart's template |
| Namespace `kagent`, labelled `ate.dev/api-access=true` | cluster | this chart's template |
| CRDs of `substrate-crds` 1.0.0 and `kagent-crds` 1.0.1 | cluster | copied into `chart/kagent/crds/` |

The CRD charts are not dependencies. Their CRDs sit in `templates/`, and the
`WorkerPool`, `SandboxConfig` and `Harness` objects in the same release need
them first. `scripts/sync-crds.sh` copies them. `substrate-atelet` is not used:
the `substrate` chart's built-in atelet is the default.

The other files in the repo:

- `chart/addon-config-definition.yaml`: the source of the addon's
  AddonConfigDefinition (ACD). `scripts/embed-acd.sh` writes it into
  `Chart.yaml` as the `addons.kubernetes.vmware.com/addon-config-definition`
  annotation, which VKS 3.8 and later read.
- `install/addonrepository.yaml`: the AddonRepository and
  AddonRepositoryInstall that register the chart as the addon `kagent`.
- `bootstrap/`: the Go program the bootstrap Job runs.

## Data flow

1. An administrator applies the AddonRepository. The addon manager reads the
   chart from `oci://ghcr.io/warroyo/vks-kagent-addon/charts` and creates the
   Addon `kagent` and an AddonRelease for each version in `addonFilters`.
2. An AddonInstall selects clusters labelled
   `addons.kubernetes.vmware.com/kagent=enabled`. The addon manager creates a
   ClusterAddon and an AddonConfig for each one.
3. The ACD renders one values Secret per cluster, `<cluster>-kagent-values` in
   `vmware-system-helm`. The Secret holds the chart's whole values file: the
   tenant's AddonConfig values, plus every derived value.
4. helm-controller installs the chart in the cluster from that Secret. Order
   inside the release:
   - The bootstrap Job and its RBAC run as pre-install and pre-upgrade hooks,
     so the CA Secrets exist before any pod starts.
   - Helm applies Namespaces and the CRDs in `crds/` before other resources.
   - PostgreSQL bootstraps itself. The Substrate API and the kagent controller
     retry until their database answers. helm-controller waits for readiness
     and retries the release (`RetryOnFailure`).
   - There is no post-install hook. Helm runs those only after everything is
     Ready, which would deadlock a Job that something waits on.

The ACD can read the Cluster resource. It takes the cluster DNS domain from
`spec.clusterNetwork.serviceDomain` (`cluster.local` when absent). A Helm parent
chart cannot compute values for its subcharts, and the ACD can, which is why the
wiring lives there.

## Names and namespaces

The release must be named `kagent`. On VKS the HelmRelease's release name is the
addon name, and its namespace is `helmOptions.targetNamespace` (default
`ate-system`).

The Substrate charts have no name or namespace overrides. They prefix every
object with the release name, and use `.Release.Namespace` throughout. With the
release `kagent` in `ate-system`, Substrate renders `kagent-api`, `kagent-idp`
and `kagent-atenet-router`, the service accounts `kagent-ate-api-server` and
`kagent-atelet`, the SPIFFE IDs `spiffe://cluster.local/ns/ate-system/sa/kagent-*`
and the issuer `https://kagent-idp.ate-system.svc`.

The values that point at those names are derived. The ACD sets them and
`values.yaml` repeats them so `helm template` and `helm lint` work without the
ACD. `test/acd/acd_test.go` checks the two agree.

| Derived value | Result |
|---|---|
| `kagent.config.substrate.apiEndpoint` | `dns:///kagent-api.ate-system.svc:443` |
| `kagent.config.substrate.routerUrl` | `https://kagent-atenet-router.ate-system.svc:443` |
| `kagent.config.substrate.actorJwtIssuer` | `https://kagent-idp.ate-system.svc` |
| `substrate.ateApi.authorization.bootstrapOwners` | kagent's controller, and `system:serviceaccount:ate-system:kagent-ate-client` for `kubectl ate` |
| `substrate.atenetRouter.ingressAllowedSPIFFEIDs` | `spiffe://cluster.local/ns/kagent/sa/kagent-controller` |
| `substrate-podcert.postgresClients` | kagent's controller, for both kagent logins |
| `substrate.credentialProvider.namespacePolicies` | atespace `kagent` may read Secrets in namespace `kagent` |
| `substrate.atelet.extraArgs` | `--ateapi-address` and `--ateapi-server-name` for `kagent-api` |
| `global.clusterDomain` | from the Cluster resource |

The chart passes the other prefixed addresses itself, but not atelet's
`--ateapi-address` and `--ateapi-server-name`, which default to
`api.ate-system.svc`. The ACD sets them through `substrate.atelet.extraArgs`.

The SPIFFE trust domain stays `cluster.local` whatever the cluster's DNS
domain. Substrate's charts hardcode it in the flags that check identities.

The pod certificate controller runs in `ate-system`, not in
`podcertificate-controller-system` as in kagent's own install. It has no ports
and reads its CA pools from Secrets mounted in its own namespace, so Substrate's
default-deny ingress policy does not affect it.

## Bootstrap Job

`bootstrap/` is adapted from kagent's `substrate_init.go` (Apache-2.0, with
attribution in the file). kagent's version hardcodes the namespaces of the CA
pools. This one writes to the release namespace. It creates, only when absent:

- the three pod certificate controller CA pools, `actor-id-ca-pool`,
  `egress-mitm-ca-pool`, `actor-id-jwt-pool` and `actor-id-ca-certs`
- the RustFS credentials Secret, `rustfs-credentials`, with random keys

It never rotates anything. It refreshes only the JWT pool's derived public keys.
It also sets the Pod Security labels on the release namespace. That cannot be a
Namespace template, because Helm does not own the namespace. A ClusterRole
limited by `resourceNames` to that one namespace allows it. The Job creates no
namespace.

The Job's ServiceAccount and Role are hooks with a lower weight, so they exist
before the Job and go away with it. The CAs expire one year after creation and
nothing renews them.

## PostgreSQL

One instance in `kagent`, `postgres:18-alpine` pinned by digest, the image
kagent's own install uses. It holds kagent's and Substrate's schemas and roles.

- Clients authenticate with pod certificates (signer `postgres.podcert.ate.dev`)
  and `hostssl ... cert` rules. There are no passwords.
- A native sidecar, `db-setup`, runs both setup scripts
  (`chart/kagent/files/kagent-postgres-setup.sql` and
  `substrate-postgres-setup.sql`) on start. It creates the `kagent` database
  when it is missing.
- The podcert controller issues each client its login certificate. Substrate's
  logins go to `kagent-ate-api-server`. kagent's go to
  `kagent/kagent-controller`.

## RustFS

RustFS stores actor snapshots, in the bucket `ate-snapshots`. The `substrate`
chart's own RustFS takes its keys as plain values, and VKS helm values are
plain text in the AddonConfig. So `substrate.rustfs.enabled` is `false` and this
chart renders its own RustFS. It reads the Secret the bootstrap Job generated.
`substrate.objectStorePlugin.extraEnv` hands that Secret to the Substrate API
and atelet. A Job creates the bucket and retries until RustFS answers.

The snapshot location is `gs://ate-snapshots/`. The S3 backend uses `gs://` URIs.

## Egress CA trust

Substrate's egress gateway verifies upstream TLS only against the
`ca-certificates.crt` in its image, and its config is fixed by the subchart. So
the trust store is replaced from outside, declaratively:

- The chart renders the ConfigMap `egress-ca-bundle` in `ate-system`. It holds
  Mozilla's public roots (`chart/kagent/files/ca-roots.pem`) plus
  `egressUpstreamTrust.caBundle` when set. It is always rendered.
- The ACD emits a MutatingAdmissionPolicy and its binding, as cluster-scoped
  outputs. They mount the ConfigMap over the gateway's bundle in every gateway
  pod. The addon manager applies them before the HelmRelease runs and removes
  them with the addon.

Without a private CA, the gateway trusts the chart's roots instead of its
image's. The pods read the bundle only at start, so after a change run
`kubectl -n ate-system delete pod -l app=atenet-egress`. See the
[FAQ](faq.md#how-do-i-trust-a-private-ca-for-egress) and
[Decisions](decisions.md#the-egress-gateways-extra-ca-is-built-declaratively).

## Pod Security

VKS enforces `restricted` cluster-wide by default. Substrate's workers cannot
pass it. The atecontroller builds worker pods that run as root, mount a
hostPath, add `SYS_ADMIN`, `NET_ADMIN`, `SETUID`, `SETGID` and `DAC_OVERRIDE`,
and set an Unconfined AppArmor profile (microvm workers also an Unconfined
seccomp profile). Atelet needs root, a hostPath and the extra capabilities.

So the namespaces that hold workers or atelet get `privileged`, set by
`podSecurity.level`:

- `ate-system`, labelled by the bootstrap Job
- `kagent` and the namespaces of extra WorkerPools, labelled by chart templates

Everything else the chart renders passes `restricted`, and the bootstrap Job's
pod does too, because it runs before the label exists.

## NetworkPolicy workaround

The `substrate` subchart's NetworkPolicy `kagent-k8s-credential-provider` admits
ingress only from pods labelled `app=kagent-atenet-egress`. The egress gateway
pods are labelled `app=atenet-egress`, so egress credential injection cannot
connect. The chart adds `kagent-k8s-credential-provider-from-egress`, which
admits the missing source on TCP 50051. NetworkPolicies are additive. Remove it
when kagent-dev/substrate#179 is fixed in the subchart.

Every place the chart works around an upstream problem is listed in
[Upstream workarounds](upstream-workarounds.md).

## Failure modes

- Wrong release name or namespace: the prefixed names no longer match the
  derived values, and kagent cannot reach Substrate. Do not rename the addon.
- Egress gateway fails upstream TLS with `invalid peer certificate:
  UnknownIssuer`: the endpoint uses a private CA that is not in
  `egressCABundle`.
- Egress gateway pods hang in `ContainerCreating`: the policy mounts
  `egress-ca-bundle` and the ConfigMap is missing. The chart always renders it,
  so this means the release did not install.
- Reinstall fails once on the SandboxConfig, and removal leaves resources
  behind: see the [FAQ](faq.md).
- A one-shot failure in the ordering does not converge. That is why only
  pre-install hooks are used and `RetryOnFailure` is on.
- The CAs expire one year after creation. Nothing renews them.
