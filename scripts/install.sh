#!/bin/sh
# observer installer — installs the `observer` Docker wrapper (scripts/observer)
# to a directory on PATH. The only runtime dependency is Docker; this script
# itself needs nothing but POSIX sh, curl (or an already-local checkout).
set -eu

REPO_RAW_BASE="${OBSERVER_RAW_BASE:-https://raw.githubusercontent.com/taikuri-infra/observer/main}"
DEST_PRIMARY="${HOME}/.local/bin"
DEST_FALLBACK="/usr/local/bin"

say() { printf '%s\n' "$*"; }
err() { printf 'observer: %s\n' "$*" >&2; }

if ! command -v docker >/dev/null 2>&1; then
  err "Docker is required but was not found on PATH."
  err "Install Docker, then re-run this script: https://docs.docker.com/get-docker/"
  exit 1
fi

# Prefer the wrapper already checked out alongside this script (running
# from a cloned repo); fall back to downloading it from GitHub.
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
local_wrapper="${script_dir}/observer"

install_to() {
  dest_dir="$1"
  mkdir -p "$dest_dir" 2>/dev/null || return 1
  [ -w "$dest_dir" ] || return 1
  dest="${dest_dir}/observer"
  if [ -f "$local_wrapper" ]; then
    cp "$local_wrapper" "$dest"
  elif command -v curl >/dev/null 2>&1; then
    curl -fsSL "${REPO_RAW_BASE}/scripts/observer" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "${REPO_RAW_BASE}/scripts/observer" -O "$dest"
  else
    err "need curl or wget to download the observer wrapper"
    return 1
  fi
  chmod +x "$dest"
  say "installed: $dest"
  return 0
}

if install_to "$DEST_PRIMARY"; then
  installed_dir="$DEST_PRIMARY"
elif install_to "$DEST_FALLBACK"; then
  installed_dir="$DEST_FALLBACK"
else
  err "could not write to $DEST_PRIMARY or $DEST_FALLBACK (try: sudo sh install.sh)"
  exit 1
fi

say ""
say "observer installed to ${installed_dir}/observer"
case ":$PATH:" in
  *":${installed_dir}:"*) ;;
  *) say "NOTE: ${installed_dir} is not on your PATH — add it, e.g.:" ;
     say "  export PATH=\"${installed_dir}:\$PATH\"" ;;
esac
say ""
say "Next steps:"
say "  cd /path/to/your/repo"
say "  observer up            # pulls ghcr.io/taikuri-infra/observer and opens http://127.0.0.1:7979"
say "  observer init          # scaffold obs-data/observer.yaml (auto-detected tabs)"
say "  observer doctor        # check which analyzer tools are available"
