# Configuration

## AddonConfig values

Set these under `spec.values` of the AddonConfig `<cluster>-kagent` in the
cluster's Supervisor namespace. See `examples/addonconfig.yaml`. All are
optional. The source is the schema in `chart/addon-config-definition.yaml`.

| Value | Default | |
|---|---|---|
| `helmOptions.targetNamespace` | `ate-system` | Namespace of the release. Substrate, the pod certificate controller, RustFS and the bootstrap Job go here. kagent and PostgreSQL go to `kagent`. |
| `workerPools[].name` | | Required for each pool. Name of the WorkerPool and its worker ServiceAccount. |
| `workerPools[].namespace` | `kagent` | Namespace of the pool. The chart creates it when missing. |
| `workerPools[].replicas` | `2` | Worker pods, 1 to 100. Pools are in addition to kagent's own `kagent-default`. |
| `snapshotStorageSize` | `20Gi` | Volume for actor snapshots (RustFS). |
| `postgresStorageSize` | `10Gi` | PostgreSQL volume, shared by kagent and Substrate. |
| `storageClass` | empty | StorageClass for both volumes. Empty uses the cluster default. |
| `imageRegistry` | empty | Registry host that replaces the host of every image, a mirror that keeps repository paths. |
| `egressCABundle` | empty | PEM CA certificates the egress gateway trusts, in addition to the public roots. See the [FAQ](faq.md#how-do-i-trust-a-private-ca-for-egress). |
| `modelProvider.baseUrl` | empty | Base URL of an OpenAI-compatible endpoint. Empty uses OpenAI's. |
| `modelProvider.model` | `gpt-4.1-mini` | Model name. |
| `modelProvider.apiKeySecretName` | empty | Secret in `kagent` with the key `OPENAI_API_KEY`. Empty keeps `kagent-openai`. |
| `helmValues` | | Extra Helm values for the chart, applied before the derived values. Derived keys are overwritten. |

The API key is never an AddonConfig value. See [the FAQ](faq.md#how-do-i-set-up-a-model).

## Chart values you might change

Set these through `helmValues`. Defaults are in `chart/kagent/values.yaml`.

| Value | Default | |
|---|---|---|
| `podSecurity.level` | `privileged` | Pod Security level of `kagent` and extra WorkerPool namespaces. Substrate's workers need `privileged`. |
| `bootstrap.image.registry`, `.repository`, `.tag` | `ghcr.io`, `warroyo/vks-kagent-addon/bootstrap`, empty (the chart's `appVersion`) | The bootstrap Job's image. |
| `postgres.image`, `.resources` | `postgres:18-alpine` by digest | PostgreSQL image and resources. |
| `rustfs.image`, `.resources`, `.bucket` | bucket `ate-snapshots` | Change the bucket only together with kagent's Harness snapshot paths and `substrate.ateApi.snapshotLocation`. |
| `workerPool.image` | `ghcr.io/kagent-dev/substrate/ateworker-gvisor:v1.0.0` | The worker image. Set `kagent.substrateWorkerPool.workerImage` to match. |
| `egressUpstreamTrust.caBundle` | empty | What `egressCABundle` sets. |
| `global.imagePullSecrets` | `[]` | Pull secrets for every image. |

## Derived values

The ACD sets these for each cluster. Do not set them: anything under
`helmValues` at these paths is overwritten. See
[Architecture](architecture.md#names-and-namespaces).

- `global.clusterDomain`
- `kagent.config.substrate.apiEndpoint`, `routerUrl` and `actorJwtIssuer`
- `substrate.ateApi.authorization.bootstrapOwners`
- `substrate.atenetRouter.ingressAllowedSPIFFEIDs`
- `substrate.credentialProvider.namespacePolicies`
- `substrate.atelet.extraArgs`
- `substrate-podcert.postgresClients` and `postgresClientServiceAccount`
- `substrate.preview`, set to `PerTemplateSnapshotLocation` and
  `GoldenEgressPolicy`
