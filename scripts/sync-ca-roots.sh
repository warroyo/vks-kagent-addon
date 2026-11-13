#!/usr/bin/env bash
# Downloads Mozilla's public CA bundle (as curated by curl) into the chart as
# files/ca-roots.pem. The chart appends the tenant's egressUpstreamTrust.caBundle
# to it to build the ConfigMap that the egress gateway mounts as its trust
# store, because the gateway verifies only against that one file.
#
#   scripts/sync-ca-roots.sh           refresh files/ca-roots.pem
#   scripts/sync-ca-roots.sh --check   fail if it differs from the current download (CI)
#
# The download is verified against the SHA-256 that curl.se publishes beside it.
set -euo pipefail

BUNDLE_URL=https://curl.se/ca/cacert.pem
SHA_URL=https://curl.se/ca/cacert.pem.sha256

root=$(cd "$(dirname "$0")/.." && pwd)
dest=$root/chart/kagent/files/ca-roots.pem
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL "$BUNDLE_URL" -o "$work/cacert.pem"
curl -fsSL "$SHA_URL" -o "$work/cacert.pem.sha256"

want=$(awk '{print $1}' "$work/cacert.pem.sha256")
got=$(sha256sum "$work/cacert.pem" | awk '{print $1}')
if [[ $want != "$got" ]]; then
  echo "checksum mismatch for $BUNDLE_URL: want $want, got $got" >&2
  exit 1
fi
if ! grep -q -- '-----BEGIN CERTIFICATE-----' "$work/cacert.pem"; then
  echo "$BUNDLE_URL holds no certificates" >&2
  exit 1
fi

# The bundle's own first lines say which Mozilla snapshot it is.
asof=$(grep -m1 '^## Certificate data from Mozilla as of:' "$work/cacert.pem" | sed 's/^## *//')
{
  echo "# Public CA roots: Mozilla's bundle as published by curl at $BUNDLE_URL"
  echo "# (sha256 verified). ${asof:-Snapshot date not found.}"
  echo "# Written by scripts/sync-ca-roots.sh; do not edit."
  cat "$work/cacert.pem"
} > "$work/ca-roots.pem"

if [[ ${1:-} == --check ]]; then
  diff "$work/ca-roots.pem" "$dest" >/dev/null || { echo "files/ca-roots.pem is out of date; run scripts/sync-ca-roots.sh" >&2; exit 1; }
  exit 0
fi

mkdir -p "$(dirname "$dest")"
cp "$work/ca-roots.pem" "$dest"
echo "wrote $dest ($(grep -c -- '-----BEGIN CERTIFICATE-----' "$dest") certificates; ${asof:-no date})"
