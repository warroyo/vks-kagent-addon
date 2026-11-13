# Known issues

Found in a live test on VKS 1.37.1 with chart 0.1.5.

## Reinstall can fail once on the SandboxConfig

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

## Removing the addon deletes its AddonConfig

The AddonConfig `<cluster>-kagent` is owned by the ClusterAddon, so removing the
addon deletes it and its values are lost. A reinstall gets a new, empty
AddonConfig.

For example, with `egressCABundle` gone, the egress gateway fails upstream TLS
with `invalid peer certificate: UnknownIssuer`. Keep the AddonConfig in GitOps
so it is applied again. After re-applying the CA, delete the egress pods
(`kubectl -n ate-system delete pod -l app=atenet-egress`).

## Removal leaves resources behind

Removing the addon leaves:

- The CRDs. Helm never deletes CRDs.
- The `ate-system` namespace with its Pod Security labels.
- The bootstrap Job's CA and RustFS Secrets in `ate-system`.
- The podcert ClusterTrustBundles (`*.podcert.ate.dev:identity:primary-bundle`
  and `egress-mitm.ate.dev:mitm:primary-bundle`).

A reinstall reuses the CA Secrets. Delete these only for a clean slate. The
`kagent` namespace and its Postgres data are deleted.

## The addon manager never downgrades

An AddonInstall pinned to a lower version than the installed one fails release
selection (`after-upgrade-version-check=0`). Name test builds as pre-releases of
the next version, such as `0.1.6-test.1`, which sorts below `0.1.6`. Never name
them above it.
