#!/usr/bin/env bash
set -eu

REPO="${REPO:-Andres77872/context-bridge}"
BINARY_NAME="${BINARY_NAME:-context-bridge}"
VERSION="${VERSION:-latest}"
NO_CHECKSUM="${NO_CHECKSUM:-0}"

log() { printf '%s\n' "$*" >&2; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

need_cmd_any() {
  for cmd in "$@"; do
    if command -v "$cmd" >/dev/null 2>&1; then
      return 0
    fi
  done
  die "need one of $* but none are available"
}

detect_os() {
  case "$(uname -s)" in
    Linux) printf '%s\n' linux ;;
    Darwin) printf '%s\n' darwin ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf '%s\n' amd64 ;;
    arm64|aarch64) printf '%s\n' arm64 ;;
    *) die "unsupported arch: $(uname -m)" ;;
  esac
}

http_get() {
  url="$1"
  out="${2:-}"
  auth_header=""
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    auth_header="-H \"Authorization: Bearer ${GITHUB_TOKEN}\""
  fi
  if command -v curl >/dev/null 2>&1; then
    if [ -n "$out" ]; then
      eval curl -fsSL "$auth_header" -o "\"\$out\"" "\"\$url\""
    else
      eval curl -fsSL "$auth_header" "\"\$url\""
    fi
  elif command -v wget >/dev/null 2>&1; then
    if [ -n "$out" ]; then
      eval wget -qO "\"\$out\"" "$auth_header" "\"\$url\""
    else
      eval wget -qO- "$auth_header" "\"\$url\""
    fi
  else
    die "need curl or wget"
  fi
}

parse_tag_name() {
  printf '%s' "$1" | tr -d '\n\r' \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'
}

latest_tag() {
  api_url="https://api.github.com/repos/${REPO}/releases/latest"
  response=$(http_get "$api_url")
  parse_tag_name "$response"
}

verify_checksum() {
  archive="$1"
  checksums_file="$2"
  archive_name=$(basename "$archive")
  line=$(awk -v f="$archive_name" '$2==f||$2=="./"f{print;exit}' "$checksums_file")
  if [ -z "$line" ]; then
    die "no checksum entry for $archive_name"
  fi
  (
    cd "$(dirname "$archive")"
    if command -v sha256sum >/dev/null 2>&1; then
      printf '%s\n' "$line" | sha256sum -c - >/dev/null 2>&1
    elif command -v shasum >/dev/null 2>&1; then
      printf '%s\n' "$line" | shasum -a 256 -c - >/dev/null 2>&1
    else
      die "need sha256sum or shasum for checksum verification"
    fi
  )
}

normalize_version() {
  printf '%s' "$1" | sed 's/^v//'
}

main() {
  need_cmd_any curl wget
  need_cmd_any sha256sum shasum
  need_cmd_any uname mktemp awk sed tar find head

  os=$(detect_os)
  arch=$(detect_arch)

  if [ "$VERSION" = "latest" ]; then
    tag=$(latest_tag)
    if [ -z "$tag" ]; then
      die "could not determine latest release tag"
    fi
  else
    tag="$VERSION"
    case "$tag" in
      v*) ;;
      *) tag="v$tag" ;;
    esac
  fi

  archive_version=$(normalize_version "$tag")
  archive_name="${BINARY_NAME}_${archive_version}_${os}_${arch}.tar.gz"
  checksums_name="${BINARY_NAME}_${archive_version}_checksums.txt"
  base_url="https://github.com/${REPO}/releases/download/${tag}"

  workdir=$(mktemp -d)
  trap 'rm -rf "$workdir"' EXIT HUP INT TERM

  http_get "${base_url}/${archive_name}" "$workdir/$archive_name"

  if [ "$NO_CHECKSUM" != "1" ]; then
    http_get "${base_url}/${checksums_name}" "$workdir/$checksums_name"
    verify_checksum "$workdir/$archive_name" "$workdir/$checksums_name"
    log "Checksum verified"
  fi

  tar -xzf "$workdir/$archive_name" -C "$workdir"

  if [ -f "$workdir/$BINARY_NAME" ]; then
    binary="$workdir/$BINARY_NAME"
  else
    binary=$(find "$workdir" -name "$BINARY_NAME" -type f | head -n1)
    if [ -z "$binary" ]; then
      die "could not find $BINARY_NAME in archive"
    fi
  fi

  chmod 0755 "$binary"
  log "Running hosted uninstall with temporary ${BINARY_NAME} ${tag}"
  CONTEXT_BRIDGE_UNINSTALL_EXCLUDE_PATH="$binary" "$binary" uninstall "$@"
}

main "$@"
