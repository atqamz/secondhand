#!/bin/sh
set -eu

HAND_RELEASE_TAG='@HAND_RELEASE_TAG@'
HAND_RELEASE_VERSION='@HAND_RELEASE_VERSION@'
HAND_RELEASE_COMMIT='@HAND_RELEASE_COMMIT@'
HAND_RELEASE_RUNTIME_ID='@HAND_RELEASE_RUNTIME_ID@'
HAND_RELEASE_SHA256_LINUX_AMD64='@HAND_RELEASE_SHA256_LINUX_AMD64@'
HAND_RELEASE_SHA256_LINUX_ARM64='@HAND_RELEASE_SHA256_LINUX_ARM64@'
HAND_RELEASE_SHA256_DARWIN_AMD64='@HAND_RELEASE_SHA256_DARWIN_AMD64@'
HAND_RELEASE_SHA256_DARWIN_ARM64='@HAND_RELEASE_SHA256_DARWIN_ARM64@'
HAND_RELEASE_SHA256_WINDOWS_AMD64='@HAND_RELEASE_SHA256_WINDOWS_AMD64@'
HAND_RELEASE_ASSET_LINUX_AMD64='hand-linux-amd64.tar.gz'
HAND_RELEASE_ASSET_LINUX_ARM64='hand-linux-arm64.tar.gz'
HAND_RELEASE_ASSET_DARWIN_AMD64='hand-darwin-amd64.tar.gz'
HAND_RELEASE_ASSET_DARWIN_ARM64='hand-darwin-arm64.tar.gz'

fleet="${HOME}/secondhand-fleet"
check_only=0
hand_install_dir="${HAND_INSTALL_DIR:-$HOME/.local/bin}"

log() { printf '%s\n' "$*" >&2; }
die() { log "bootstrap.sh: $*"; exit 1; }

release_placeholder_prefix=$(printf '@HAND%s' '_RELEASE_')
require_bound() {
  value=$1
  name=$2
  case "$value" in
    "${release_placeholder_prefix}"*) die "this source template is not a release-bound bootstrap asset" ;;
  esac
  [ -n "$value" ] || die "release binding $name is empty"
}

require_bound "$HAND_RELEASE_TAG" tag
require_bound "$HAND_RELEASE_VERSION" version
require_bound "$HAND_RELEASE_COMMIT" commit
require_bound "$HAND_RELEASE_RUNTIME_ID" runtime_id
require_bound "$HAND_RELEASE_SHA256_LINUX_AMD64" linux/amd64 digest
require_bound "$HAND_RELEASE_SHA256_LINUX_ARM64" linux/arm64 digest
require_bound "$HAND_RELEASE_SHA256_DARWIN_AMD64" darwin/amd64 digest
require_bound "$HAND_RELEASE_SHA256_DARWIN_ARM64" darwin/arm64 digest
require_bound "$HAND_RELEASE_SHA256_WINDOWS_AMD64" windows/amd64 digest
[ "$HAND_RELEASE_TAG" = "v$HAND_RELEASE_VERSION" ] || die "release tag and version do not agree"

case "$HAND_RELEASE_COMMIT" in
  *[!0-9a-fA-F]*) die "release commit is not hexadecimal" ;;
esac
case "${#HAND_RELEASE_COMMIT}" in
  40|64) ;;
  *) die "release commit must be a full 40- or 64-character ID" ;;
esac
case "$hand_install_dir" in
  /*) ;;
  *) die "HAND_INSTALL_DIR must be an absolute path" ;;
esac

usage() {
  cat <<'EOF'
Usage: bootstrap.sh [--fleet PATH] [--yes] [--check] [--help]

  --fleet PATH  fleet home to create or reconcile (default: $HOME/secondhand-fleet)
  --yes         accepted for compatibility; the canonical release command is already explicit
  --check       read-only: report readiness, install or mutate nothing
  --help        show this message
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --fleet)
      [ $# -ge 2 ] || die "--fleet requires a path"
      fleet=$2
      shift 2
      ;;
    --fleet=*)
      fleet=${1#--fleet=}
      shift
      ;;
    --yes) shift ;;
    --check) check_only=1; shift ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

case "$(uname -s)" in
  Linux) hand_goos=linux ;;
  Darwin) hand_goos=darwin ;;
  *) die "unsupported OS $(uname -s); use bootstrap.ps1 on Windows" ;;
esac

case "$(uname -m)" in
  x86_64|amd64) hand_goarch=amd64 ;;
  arm64|aarch64) hand_goarch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

case "$hand_goos/$hand_goarch" in
  linux/amd64)
    hand_asset=$HAND_RELEASE_ASSET_LINUX_AMD64
    hand_want=$HAND_RELEASE_SHA256_LINUX_AMD64
    ;;
  linux/arm64)
    hand_asset=$HAND_RELEASE_ASSET_LINUX_ARM64
    hand_want=$HAND_RELEASE_SHA256_LINUX_ARM64
    ;;
  darwin/amd64)
    hand_asset=$HAND_RELEASE_ASSET_DARWIN_AMD64
    hand_want=$HAND_RELEASE_SHA256_DARWIN_AMD64
    ;;
  darwin/arm64)
    hand_asset=$HAND_RELEASE_ASSET_DARWIN_ARM64
    hand_want=$HAND_RELEASE_SHA256_DARWIN_ARM64
    ;;
  *) die "unsupported platform $hand_goos/$hand_goarch" ;;
esac

case "$hand_want" in
  *[!0-9a-fA-F]*|'') die "invalid embedded release digest for $hand_asset" ;;
esac
[ "${#hand_want}" -eq 64 ] || die "invalid embedded release digest for $hand_asset"

output_field() {
  printf '%s\n' "$2" | awk -F': ' -v wanted="$1" '$1 == wanted { print substr($0, length(wanted) + 3); exit }'
}

verify_hand_identity() {
  hand_path=$1
  if ! hand_identity=$("$hand_path" build-info 2>&1); then
    log "$hand_identity"
    die "selected Hand executable failed its pure build identity query"
  fi
  [ "$(output_field version "$hand_identity")" = "$HAND_RELEASE_VERSION" ] || {
    log "$hand_identity"
    die "selected Hand version does not match release $HAND_RELEASE_VERSION"
  }
  [ "$(output_field channel "$hand_identity")" = stable ] || {
    log "$hand_identity"
    die "selected Hand channel is not stable"
  }
  actual_commit=$(output_field commit "$hand_identity" | tr '[:upper:]' '[:lower:]')
  expected_commit=$(printf '%s\n' "$HAND_RELEASE_COMMIT" | tr '[:upper:]' '[:lower:]')
  [ "$actual_commit" = "$expected_commit" ] || {
    log "$hand_identity"
    die "selected Hand commit does not match release $HAND_RELEASE_COMMIT"
  }
  [ "$(output_field distribution "$hand_identity")" = github ] || {
    log "$hand_identity"
    die "selected Hand distribution is not github"
  }
}

absolute_path() {
  path=$1
  path_dir=$(dirname "$path")
  path_base=$(basename "$path")
  path_dir=$(cd "$path_dir" 2>/dev/null && pwd -P) || return 1
  printf '%s/%s\n' "$path_dir" "$path_base"
}

find_hand() {
  if [ -e "$hand_install_dir/hand" ] || [ -L "$hand_install_dir/hand" ]; then
    absolute_path "$hand_install_dir/hand"
    return
  fi
  discovered_hand=$(command -v hand 2>/dev/null || true)
  [ -n "$discovered_hand" ] || return 1
  case "$discovered_hand" in
    /*) ;;
    *) return 1 ;;
  esac
  absolute_path "$discovered_hand"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print tolower($1)}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print tolower($1)}'
  else
    die "sha256sum or shasum is required to verify the Hand release"
  fi
}

download() {
  destination=$1
  url=$2
  curl -fsSL --retry 2 --connect-timeout 15 --max-time 600 -o "$destination" "$url" ||
    die "download failed: $url"
  [ -s "$destination" ] || die "download was empty: $url"
}

verify_release_archive() {
  hand_got=$(sha256_file "$1")
  [ "$hand_got" = "$(printf '%s\n' "$hand_want" | awk '{print tolower($1)}')" ] ||
    die "digest mismatch for $hand_asset: want $hand_want, got $hand_got"
}

hand_tmp=''
cleanup() {
  if [ -n "$hand_tmp" ]; then
    rm -rf "$hand_tmp"
  fi
}
trap cleanup EXIT HUP INT TERM

hand_command=''
hand_available=0

if [ "$check_only" -eq 1 ]; then
  if hand_command=$(find_hand); then
    hand_available=1
    if hand_identity=$("$hand_command" build-info 2>&1); then
      log "hand identity (check mode: no changes made):"
      log "$hand_identity"
      log "private runtime status (check mode: no changes made):"
      (cd / && HAND_HOME= "$hand_command" runtime status) || true
    else
      log "$hand_identity"
      die "hand build identity is unavailable (check mode: no changes made)"
    fi
  else
    log "hand: not installed (check mode: no changes made)"
  fi
else
  hand_tmp=$(mktemp -d) || die "could not create a temporary directory for Hand"
  hand_base="https://github.com/atqamz/hand/releases/download/$HAND_RELEASE_TAG"
  download "$hand_tmp/$hand_asset" "$hand_base/$hand_asset"
  verify_release_archive "$hand_tmp/$hand_asset"
  tar -xzf "$hand_tmp/$hand_asset" -C "$hand_tmp" hand ||
    die "could not extract the verified Hand release"
  hand_source=$hand_tmp/hand
  [ -f "$hand_source" ] || die "verified release archive does not contain hand"
  chmod 755 "$hand_source" || die "could not make the staged Hand executable"

  adopt_out=$("$hand_source" adopt \
    --source "$hand_source" \
    --target "$hand_install_dir/hand" \
    --version "$HAND_RELEASE_VERSION" \
    --commit "$HAND_RELEASE_COMMIT" 2>&1) || {
    log "$adopt_out"
    die "exact Hand adoption failed; no Fleet or runtime mutation was attempted"
  }
  log "$adopt_out"
  hand_command=$(output_field path "$adopt_out")
  [ -n "$hand_command" ] || die "exact Hand adoption returned no selected executable path"
  case "$hand_command" in
    /*) ;;
    *) die "exact Hand adoption returned a non-absolute executable path" ;;
  esac
  [ -x "$hand_command" ] || die "selected Hand executable is not runnable: $hand_command"
  verify_hand_identity "$hand_command"
  hand_available=1
fi

ensure_private_runtime() {
  if [ "$check_only" -eq 1 ]; then
    if [ "$hand_available" -eq 0 ]; then
      log "private runtime: not checked because Hand is absent (check mode: no changes made)"
    fi
    return
  fi
  [ "$hand_available" -eq 1 ] || die "Hand was not installed"
  runtime_out=$(cd / && HAND_HOME= "$hand_command" runtime ensure 2>&1) || {
    log "$runtime_out"
    die "private runtime is not ready; repair with: $hand_command runtime ensure"
  }
  runtime_actual=$(output_field runtime_id "$runtime_out")
  [ "$runtime_actual" = "$HAND_RELEASE_RUNTIME_ID" ] || {
    log "$runtime_out"
    die "private runtime identity mismatch: want $HAND_RELEASE_RUNTIME_ID, got ${runtime_actual:-none}"
  }
  log "ensuring private pinned Git, Treehouse, and Herdr runtime for $HAND_RELEASE_VERSION ($HAND_RELEASE_RUNTIME_ID)"
  log "$runtime_out"
}

ensure_private_runtime

if [ "$check_only" -eq 1 ]; then
  if [ "$hand_available" -eq 0 ]; then
    log "fleet target: not checked because Hand is absent (check mode: no changes made)"
    exit 0
  fi
  if [ ! -e "$fleet" ]; then
    log "fleet target: $fleet (absent; check mode: no changes made)"
    exit 0
  fi
  if ! doctor_out=$(HAND_HOME="$fleet" "$hand_command" doctor --fail-if-not-ready 2>&1); then
    log "$doctor_out"
    die "hand doctor reported that $fleet is not ready"
  fi
  log "$doctor_out"
  exit 0
fi

init_out=$(HAND_HOME= "$hand_command" init "$fleet" 2>&1) || {
  log "$init_out"
  die "hand init refused or failed for $fleet; resolve the reported error, then rerun bootstrap.sh --fleet $fleet"
}
log "$init_out"

doctor_out=$(HAND_HOME="$fleet" "$hand_command" doctor --fail-if-not-ready 2>&1) || {
  log "$doctor_out"
  die "hand doctor reported that $fleet is not ready; rerun HAND_HOME=$fleet $hand_command doctor after recovery"
}
log "$doctor_out"
