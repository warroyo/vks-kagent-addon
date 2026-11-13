# FAQ

Tested on VKS 1.37.1 with chart 0.1.5.

## How do I set up a model?

kagent's default ModelConfig is `default-model-config` in the `kagent`
namespace. For an OpenAI-compatible endpoint, set `modelProvider` in the
AddonConfig (see [Configuration](configuration.md)) and create the API key
Secret yourself:

```sh
kubectl -n kagent create secret generic model-api-key \
  --from-literal=OPENAI_API_KEY=...
```

The Secret name must match `modelProvider.apiKeySecretName`. Never put the key
in the AddonConfig. To manage the Secret from GitOps, create it with External
Secrets.

To add another model, create a ModelConfig in `kagent`:

```yaml
apiVersion: api.kagent.dev/v1alpha3
kind: ModelConfig
metadata:
  name: my-model
  namespace: kagent
spec:
  provider: OpenAI
  model: my-model
  apiKeySecret: model-api-key
  apiKeySecretKey: OPENAI_API_KEY
  openAI:
    baseUrl: https://models.example.com/v1
```

Agents reach the model through Substrate's egress gateway, which injects the key
from the Secret. The endpoint must be a DNS name, not an IP address, and the
egress policy must allow it.

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

On removal, Substrate's `sandboxconfig-protection` finalizer keeps the
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
- The `ate-system` namespace with its Pod Security labels.
- The bootstrap Job's CA and RustFS Secrets in `ate-system`.
- The podcert ClusterTrustBundles (`*.podcert.ate.dev:identity:primary-bundle`
  and `egress-mitm.ate.dev:mitm:primary-bundle`).

A reinstall reuses the CA Secrets. Delete these only for a clean slate. The
`kagent` namespace and its Postgres data are deleted.

## Why does the addon manager reject a lower version?

The addon manager never downgrades. An AddonInstall pinned to a lower version
than the installed one fails release selection (`after-upgrade-version-check=0`).
Name test builds as pre-releases of the next version, such as `0.1.6-test.1`,
which sorts below `0.1.6`. Never name them above it. See
[Development](development.md#test-builds).
