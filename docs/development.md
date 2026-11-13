# Development

## Tools

`go` (the version in `bootstrap/go.mod`), `helm`, `yq` and `docker`.

## Bootstrap image

Build from `bootstrap/`:

```sh
cd bootstrap
docker build -t ghcr.io/warroyo/vks-kagent-addon/bootstrap:0.1.5 .
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
