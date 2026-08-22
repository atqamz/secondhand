#!/bin/sh
set -eu

usage() {
  printf '%s\n' "usage: $0 <tag> <version> <commit> <output-dir>" >&2
  exit 2
}

[ "$#" -eq 4 ] || usage
tag=$1
version=$2
commit=$3
output=$4
root=$(cd -- "$(dirname -- "$0")/../.." && pwd)

case "$tag" in
  v[0-9]*) ;;
  *) printf '%s\n' "prepare-release.sh: tag must be a stable v-prefixed release tag" >&2; exit 1 ;;
esac
[ "$tag" = "v$version" ] || {
  printf '%s\n' "prepare-release.sh: tag $tag does not bind to version $version" >&2
  exit 1
}
case "$version" in
  ''|*[!0-9A-Za-z.+-]*) printf '%s\n' "prepare-release.sh: invalid release version $version" >&2; exit 1 ;;
esac
case "$commit" in
  *[!0-9a-fA-F]*) printf '%s\n' "prepare-release.sh: commit must be hexadecimal" >&2; exit 1 ;;
esac
case "${#commit}" in
  40|64) ;;
  *) printf '%s\n' "prepare-release.sh: commit must be a full 40- or 64-character ID" >&2; exit 1 ;;
esac
[ -d "$output" ] || {
  printf '%s\n' "prepare-release.sh: output directory does not exist: $output" >&2
  exit 1
}

runtime_lock=$root/internal/toolchain/runtime.lock.json
runtime_id=$(sed -n 's/^[[:space:]]*"runtime_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$runtime_lock" | head -n 1)
case "$runtime_id" in
  r[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]) ;;
  *) printf '%s\n' "prepare-release.sh: runtime lock has no valid runtime_id" >&2; exit 1 ;;
esac

for asset in \
  hand-linux-amd64.tar.gz \
  hand-linux-arm64.tar.gz \
  hand-darwin-amd64.tar.gz \
  hand-darwin-arm64.tar.gz \
  hand-windows-amd64.zip; do
  [ -f "$output/$asset" ] || {
    printf '%s\n' "prepare-release.sh: missing platform asset $asset" >&2
    exit 1
  }
done

sha256_asset() {
  digest=$(sha256sum "$output/$1" | cut -d ' ' -f 1 | tr -d '\r')
  [ "${#digest}" -eq 64 ] || {
    printf '%s\n' "prepare-release.sh: checksum for $1 is not a 64-character digest" >&2
    exit 1
  }
  case "$digest" in
    *[!0-9a-fA-F]*) printf '%s\n' "prepare-release.sh: checksum for $1 is not hexadecimal" >&2; exit 1 ;;
  esac
  printf '%s\n' "$digest"
}

digest_linux_amd64=$(sha256_asset hand-linux-amd64.tar.gz)
digest_linux_arm64=$(sha256_asset hand-linux-arm64.tar.gz)
digest_darwin_amd64=$(sha256_asset hand-darwin-amd64.tar.gz)
digest_darwin_arm64=$(sha256_asset hand-darwin-arm64.tar.gz)
digest_windows_amd64=$(sha256_asset hand-windows-amd64.zip)

render() {
  template=$1
  destination=$2
  grep -q '@HAND_RELEASE_TAG@' "$root/$template" || {
    printf '%s\n' "prepare-release.sh: template $template has no release binding" >&2
    exit 1
  }
  sed \
    -e "s|@HAND_RELEASE_TAG@|$tag|g" \
    -e "s|@HAND_RELEASE_VERSION@|$version|g" \
    -e "s|@HAND_RELEASE_COMMIT@|$commit|g" \
    -e "s|@HAND_RELEASE_RUNTIME_ID@|$runtime_id|g" \
    -e "s|@HAND_RELEASE_SHA256_LINUX_AMD64@|$digest_linux_amd64|g" \
    -e "s|@HAND_RELEASE_SHA256_LINUX_ARM64@|$digest_linux_arm64|g" \
    -e "s|@HAND_RELEASE_SHA256_DARWIN_AMD64@|$digest_darwin_amd64|g" \
    -e "s|@HAND_RELEASE_SHA256_DARWIN_ARM64@|$digest_darwin_arm64|g" \
    -e "s|@HAND_RELEASE_SHA256_WINDOWS_AMD64@|$digest_windows_amd64|g" \
    "$root/$template" > "$destination.tmp"
  if grep -q '@HAND_RELEASE_' "$destination.tmp"; then
    rm -f "$destination.tmp"
    printf '%s\n' "prepare-release.sh: unresolved release binding in $template" >&2
    exit 1
  fi
  mv "$destination.tmp" "$destination"
}

render bootstrap.sh "$output/bootstrap.sh"
render bootstrap.ps1 "$output/bootstrap.ps1"
chmod 755 "$output/bootstrap.sh"

rm -f "$output/release-manifest.json"

(cd "$output" && sha256sum --text \
  hand-linux-amd64.tar.gz \
  hand-linux-arm64.tar.gz \
  hand-darwin-amd64.tar.gz \
  hand-darwin-arm64.tar.gz \
  hand-windows-amd64.zip \
  bootstrap.sh \
  bootstrap.ps1) > "$output/checksums.txt.tmp"
mv "$output/checksums.txt.tmp" "$output/checksums.txt"
