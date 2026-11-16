# Development

## Tools

`go` (the version in `bootstrap/go.mod`), `helm`, `yq` and `docker`.

## Bootstrap image

Build from `bootstrap/`:

```sh
cd bootstrap
docker build -t ghcr.io/warroyo/vks-kagent-addon/bootstrap:0.1.6 .
```

The tag is the chart's `appVersion`. The chart's `bootstrap.image.tag` defaults
to it. CI builds and pushes the image on release.

## Tests

```sh
(cd bootstrap && go vet ./... && go test ./...)
helm dependency build chart/kagent
(cd test/acd && go test ./...)
helm lint chart/kagent
```

`test/acd` renders `chart/addon-config-definition.yaml` the way the addon
controller does, checks the derived values, and feeds the result to
`helm template`. That is why it needs `helm` and the chart dependencies. The
controller reports a template error only per cluster after install, in a chart
version that cannot be re-published, so this is the fast loop. Set
`ACD_DUMP_VALUES=<file>` to keep the rendered values.

CI runs the same commands, plus the two checks below, in `.github/workflows/test.yaml`.

## Check and sync scripts

| Script | Does |
|---|---|
| `scripts/embed-acd.sh` | Writes `chart/addon-config-definition.yaml` into `Chart.yaml` as the ACD annotation, named `kagent.<chart version>`. Run it after editing the ACD or the version. `--check` fails when the annotation is stale. |
| `scripts/sync-crds.sh` | Copies the CRDs of the pinned `substrate-crds` and `kagent-crds` charts into `chart/kagent/crds/`. Bump the versions in the script together with the dependencies in `Chart.yaml`. `--check` fails when `crds/` differs. |
| `scripts/sync-ca-roots.sh` | Downloads Mozilla's public roots from curl.se into `chart/kagent/files/ca-roots.pem`, verified against the published SHA-256. |

CI runs `embed-acd.sh --check` and `sync-crds.sh --check`. It does not run
`sync-ca-roots.sh --check`: that compares against the live bundle and would fail
whenever Mozilla updates it. Refresh the roots by hand with
`scripts/sync-ca-roots.sh` and release a new chart version. The shipped roots go
stale until you do.

## Test cluster

A VKS 1.37 cluster with the published chart installed. Steps 1 and 3 need a
Supervisor administrator.

1. Create a Supervisor Namespace, here `kagent-test`. Assign the VM classes
   `best-effort-medium` and `best-effort-large`, a storage policy, and a content
   library with the 1.37.1 VKr. 300Gi of storage, 30 GHz of CPU and 64Gi of
   memory are enough.
2. Set the storage class in `test/cluster.yaml`, then create the cluster:

   ```sh
   kubectl -n kagent-test apply -f test/cluster.yaml
   ```

3. Offer the addon on the Supervisor:

   ```sh
   kubectl apply -f install/addonrepository.yaml
   kubectl -n vmware-system-vks-public get addonrepositoryinstall kagent-oci-repo-install
   ```

   The Ready condition lists the valid AddonReleases, such as
   `kagent:0.1.6, 0.1.5`.
4. Install the addon. Apply `examples/addoninstall.yaml` with `namespace` set
   to `kagent-test`, and label the cluster:

   ```sh
   kubectl -n kagent-test label cluster kagent-test addons.kubernetes.vmware.com/kagent=enabled
   ```

   The cluster also needs the helm-controller addon. Clusters from
   `builtin-generic-v3.8.0` already have the label
   `addon.addons.kubernetes.vmware.com/helm-controller=automatic`.
   To test a build from [Test builds](#test-builds), add its version to
   `addonFilters` and set `releaseFilter` in the AddonInstall to
   `kagent.<version>`.
5. When the cluster is Provisioned, get its kubeconfig and check the install:

   ```sh
   kubectl -n kagent-test get secret kagent-test-kubeconfig -o jsonpath='{.data.value}' | base64 -d > kagent-test.kubeconfig
   chmod 600 kagent-test.kubeconfig
   export KUBECONFIG=$PWD/kagent-test.kubeconfig
   kubectl -n vmware-system-helm get helmrelease kagent-test-kagent
   kubectl -n ate-system get pods
   kubectl -n kagent get pods
   ```

   The HelmRelease reports `Helm install succeeded`. `kagent-controller`
   restarts a few times while PostgreSQL starts.
6. Connect a model and run an agent. See
   [Models and agents](models-and-agents.md).

The addon manager creates the AddonConfig `kagent-test-kagent` with the
install. To change values, such as `egressCABundle` or `helmValues`, patch it:

```sh
kubectl -n kagent-test patch addonconfig kagent-test-kagent --type merge \
  -p '{"spec":{"values":{"helmValues":{"kagent":{"ui":{"service":{"type":"LoadBalancer"}}}}}}}'
```

The HelmRelease in the guest cluster checks for new values every 10 minutes. To
apply them now:

```sh
kubectl -n vmware-system-helm annotate helmrelease kagent-test-kagent \
  reconcile.fluxcd.io/requestedAt="$(date +%s)" --overwrite
```

To tear down, delete in this order, so that the addon manager removes the addon
from the cluster first:

1. The AddonInstall. Wait until ClusterAddon `kagent-test-kagent` is gone.
2. The cluster.
3. The Supervisor Namespace.

Delete the AddonRepository only when no cluster on the Supervisor uses the
addon.

## Releasing

1. Bump `version` and `appVersion` in `chart/kagent/Chart.yaml`.
2. Run `scripts/embed-acd.sh`, because the ACD name carries the version.
3. Update the version in `examples/` (`kagent.<version>`) and add it to
   `addonFilters` in `install/addonrepository.yaml`. Keep older versions while
   clusters still use them. An installed AddonRepository allows only
   `addonFilters` to change.
4. Merge to `main`. `.github/workflows/release.yaml` checks the embedded ACD and
   the CRDs, builds and pushes the bootstrap image (tag `appVersion`), then
   packages and pushes the chart to `oci://ghcr.io/warroyo/vks-kagent-addon/charts`,
   and creates the GitHub release `kagent-<version>`.

The workflow skips an image tag or chart version that is already published. It
never overwrites one. Apply the updated `install/addonrepository.yaml` on the
Supervisor to offer the new version.

### Public packages

GitHub creates packages private on the first push. After the first release, make
`vks-kagent-addon/bootstrap` and `vks-kagent-addon/charts/kagent` public in the
package settings, or the Supervisor and guest clusters cannot pull them
anonymously. Do it once for each package.

### Test builds

The addon manager never downgrades. Name a test build as a pre-release of the
next version, such as `0.1.6-test.1`, which sorts below `0.1.6`. Never name it
above the version you will release. A build installed under a higher name blocks
every later release below it. Test builds are pushed by hand: the workflow
releases only the version in `Chart.yaml`.
