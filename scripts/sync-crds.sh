#!/usr/bin/env bash
# Copies the CRDs of the pinned substrate-crds and kagent-crds charts into the
# umbrella chart's crds/ directory. Neither chart is a dependency: their CRDs
# sit in templates/, and the WorkerPool, SandboxConfig and Harness objects in
# this same release need the CRDs to exist first. VKS 3.8 installs crds/ with
# helm-controller's `crds: CreateReplace`, so they are also updated on upgrade.
#
#   scripts/sync-crds.sh           refresh crds/
#   scripts/sync-crds.sh --check   fail if crds/ differs from the pinned charts (CI)
#
# Bump the two versions below together with the dependencies in Chart.yaml.
set -euo pipefail

SUBSTRATE_CRDS_VERSION=1.0.0
KAGENT_CRDS_VERSION=1.0.1

root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/chart/kagent/crds
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

helm pull oci://ghcr.io/kagent-dev/substrate/helm/substrate-crds --version "$SUBSTRATE_CRDS_VERSION" --untar --untardir "$work/substrate"
helm pull oci://ghcr.io/kagent-dev/kagent/helm/kagent-crds --version "$KAGENT_CRDS_VERSION" --untar --untardir "$work/kagent"

# The CRD charts are untemplated. A template action would be copied verbatim
# into crds/, which Helm does not render, so refuse it.
if grep -l '{{' "$work"/substrate/substrate-crds/templates/*.yaml "$work"/kagent/kagent-crds/templates/*.yaml; then
  echo "a CRD file contains a template action; crds/ cannot render it" >&2
  exit 1
fi

staged=$work/crds
mkdir -p "$staged"
cp "$work"/substrate/substrate-crds/templates/*.yaml "$staged/"
cp "$work"/kagent/kagent-crds/templates/*.yaml "$staged/"

if [[ ${1:-} == --check ]]; then
  diff -r "$staged" "$dest" || { echo "crds/ is out of date; run scripts/sync-crds.sh" >&2; exit 1; }
  exit 0
fi

mkdir -p "$dest"
rm -f "$dest"/*.yaml
cp "$staged"/*.yaml "$dest/"
echo "synced $(ls "$dest" | wc -l) CRDs into $dest"
