# FAQ

Tested on VKS 1.37.1 with chart 0.1.6.

## How do I set up a model?

Create a ModelConfig and an API key Secret in `kagent`, and point your agents
at the ModelConfig. See [Models and agents](models-and-agents.md).

The AddonConfig value `modelProvider` (see [Configuration](configuration.md))
only fills in `default-model-config`, the ModelConfig kagent's built-in agents
use. The Secret it names must exist in `kagent` with the key `OPENAI_API_KEY`.
Never put the key in the AddonConfig.

## How do I reach the UI?

Port-forward `svc/kagent-ui` in `kagent` on port 8080, or make the Service a
LoadBalancer with `helmValues.kagent.ui.service.type`. See
[Models and agents](models-and-agents.md#reaching-the-ui).

## How do I trust a private CA for egress?

The egress gateway verifies upstream TLS only against its own CA bundle. If the
model endpoint uses a private CA, requests fail with `invalid peer certificate:
UnknownIssuer`.

Set `egressCABundle` in the AddonConfig to the PEM CA certificates. They are
added to the public roots the chart ships. The egress pods read the bundle only
at start, so after you set or change it, delete them:

```sh
kubectl -n ate-system delete pod -l app=atenet-egress
```

The Deployment recreates the pods and the policy mounts the new bundle. Do not
restart the Deployment: that edits its pod template, which Flux drift detection
may revert. See [Decisions](decisions.md#the-egress-gateways-extra-ca-is-built-declaratively)
and [Upstream workarounds](upstream-workarounds.md#the-egress-gateway-cannot-trust-a-private-ca).

## Why can a reinstall fail once on the SandboxConfig?

Removing the addon and reinstalling soon after can fail once with
`InstallFailed ... timeout waiting for: [SandboxConfig/gvisor-default status: 'NotFound']`.

On removal, Substrate's `workerpool-protection` finalizer keeps the
cluster-scoped SandboxConfig while WorkerPools use it. The atecontroller is
removed before it can release the object. The reinstalled controller then
releases and deletes the old object after Helm created or adopted it.

The HelmRelease's RetryOnFailure retries every 10 minutes, and the retry
succeeds. To retry now:

```
kubectl -n vmware-system-helm annotate helmrelease <cluster>-kagent \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
```

## Why did removing the addon delete my AddonConfig?

The AddonConfig `<cluster>-kagent` is owned by the ClusterAddon, so removing the
addon deletes it and its values are lost. A reinstall gets a new, empty
AddonConfig.

For example, with `egressCABundle` gone, the egress gateway fails upstream TLS
with `invalid peer certificate: UnknownIssuer`. Keep the AddonConfig in GitOps
so it is applied again. After re-applying the CA, delete the egress pods (see
[above](#how-do-i-trust-a-private-ca-for-egress)).

## What does removing the addon leave behind?

Removing the addon leaves:

- The CRDs. Helm never deletes CRDs.
- The SandboxConfig `gvisor-default`, held by its
  `sandboxconfig.ate.dev/workerpool-protection` finalizer.
- The podcert ClusterTrustBundles (`*.podcert.ate.dev:identity:primary-bundle`
  and `egress-mitm.ate.dev:mitm:primary-bundle`).

The `ate-system` namespace is an ACD output, so the addon manager deletes it,
with the bootstrap Job's CA and RustFS Secrets. The `kagent` namespace and its
Postgres data are deleted too. A reinstall generates new CAs.

For a clean slate, remove the SandboxConfig's finalizer and delete it, then
delete the ClusterTrustBundles and the `*.ate.dev` and `*.kagent.dev` CRDs.

## Why does the addon manager reject a lower version?

The addon manager never downgrades. An AddonInstall pinned to a lower version
than the installed one fails release selection (`after-upgrade-version-check=0`).
Name test builds as pre-releases of the next version, such as `0.1.6-test.1`,
which sorts below `0.1.6`. Never name them above it. See
[Development](development.md#test-builds).

## Why do components log `failed to upload metrics ... localhost:4317`?

Substrate components push OTLP metrics to `localhost:4317` when no endpoint is
set, and nothing listens there. The errors are harmless. Upstream:
kagent-dev/substrate#76 and PR #166, both open.

## When do the CAs expire?

The bootstrap Job creates the certificate authorities, and they expire one year
after creation. Nothing renews them, and an expired CA breaks every mTLS
connection. Upstream tracks reload on rotation in agent-substrate/substrate#3445,
open.
