#!/bin/sh
# lasso installer — downloads the latest release binary for your platform.
#
#   curl -fsSL https://executionassociates.com/install-lasso | sh
#
# (a Cloudflare redirect rule on the executionassociates.com zone, pointing at
# raw.githubusercontent.com/execution-associates/lasso/main/install.sh)
#
# Honors:
#   LASSO_INSTALL_DIR   where to install (default ~/.local/bin)
#   LASSO_TELEMETRY=0   turn lasso's anonymous usage analytics off without asking
#                       (same as passing --no-telemetry: `| sh -s -- --no-telemetry`)
set -eu

telemetry=""
for arg in "$@"; do
  case "$arg" in
    --no-telemetry) telemetry=off ;;
    *) echo "lasso: unknown option '$arg' (known: --no-telemetry)" >&2; exit 2 ;;
  esac
done
case "${LASSO_TELEMETRY:-}" in
  0 | false | off | no) telemetry=off ;;
esac

REPO="execution-associates/lasso"
BASE="https://github.com/${REPO}/releases/latest/download"

# --- detect platform -------------------------------------------------------
os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) echo "lasso: unsupported OS '$os' (need Linux or macOS)" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "lasso: unsupported architecture '$arch'" >&2; exit 1 ;;
esac
asset="lasso-${os}-${arch}"

# --- download --------------------------------------------------------------
dl() { # url outfile
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    echo "lasso: need curl or wget to download" >&2
    exit 1
  fi
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "lasso: downloading ${asset} …"
dl "${BASE}/${asset}" "${tmp}/${asset}"
dl "${BASE}/checksums.txt" "${tmp}/checksums.txt" || true

# --- verify checksum (when published) --------------------------------------
if [ -s "${tmp}/checksums.txt" ]; then
  want=$(awk -v a="$asset" '$2==a{print $1}' "${tmp}/checksums.txt")
  if [ -n "$want" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "${tmp}/${asset}" | awk '{print $1}')
    else
      got=$(shasum -a 256 "${tmp}/${asset}" | awk '{print $1}')
    fi
    if [ "$want" != "$got" ]; then
      echo "lasso: checksum mismatch for ${asset} (want ${want}, got ${got})" >&2
      exit 1
    fi
    echo "lasso: checksum verified"
  fi
fi

# --- install ---------------------------------------------------------------
dir="${LASSO_INSTALL_DIR:-$HOME/.local/bin}"
mkdir -p "$dir"
chmod +x "${tmp}/${asset}"
mv "${tmp}/${asset}" "${dir}/lasso"
echo "lasso: installed → ${dir}/lasso ($("${dir}/lasso" version 2>/dev/null || echo unknown))"

# --- anonymous usage analytics ---------------------------------------------
# lasso reports a few anonymous events (which views and features get used,
# rough counts, a random install id; never names, paths, prompts or anything
# typed) unless it is told not to. Asked here when someone is at a terminal;
# stored in lasso.db through `lasso telemetry on|off`, which is also how to
# change it later (or Settings > General > Anonymous usage analytics).
if [ -z "$telemetry" ] && [ -t 1 ] && { : </dev/tty; } 2>/dev/null; then
  echo
  echo "lasso can send anonymous usage analytics: which views and features get used"
  echo "and rough counts, under a random id. Never names, paths, hosts, prompts or"
  echo "anything you type. Details: https://www.executionassociates.com/privacy/"
  printf "Share anonymous usage analytics? [Y/n] "
  ans=""
  read -r ans </dev/tty || ans=""
  case "$ans" in
    [nN]*) telemetry=off ;;
    *) telemetry=on ;;
  esac
fi
if [ -n "$telemetry" ]; then
  if "${dir}/lasso" telemetry "$telemetry" >/dev/null 2>&1; then
    echo "lasso: anonymous usage analytics ${telemetry} (change it any time: lasso telemetry on|off)"
  else
    echo "lasso: couldn't record the analytics choice; run 'lasso telemetry ${telemetry}' yourself" >&2
  fi
fi

# --- PATH hint -------------------------------------------------------------
case ":${PATH}:" in
  *":${dir}:"*) ;;
  *)
    echo
    echo "note: ${dir} is not on your PATH — add it, e.g.:"
    echo "  export PATH=\"${dir}:\$PATH\""
    ;;
esac

# --- herdr prerequisite ----------------------------------------------------
# lasso is a UI over herdr, so install it automatically when missing — the whole
# point is a frictionless one-command setup. Honors LASSO_SKIP_HERDR=1 to opt out.
if ! command -v herdr >/dev/null 2>&1; then
  if [ "${LASSO_SKIP_HERDR:-}" = "1" ]; then
    echo
    echo "lasso drives herdr, which isn't installed yet. Install it with:"
    echo "  curl -fsSL https://herdr.dev/install.sh | sh"
  else
    echo
    echo "lasso: herdr not found — installing it (set LASSO_SKIP_HERDR=1 to skip) …"
    # Non-fatal: a herdr install hiccup shouldn't abort a successful lasso setup.
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL https://herdr.dev/install.sh | sh || herdr_ok=0
    elif command -v wget >/dev/null 2>&1; then
      wget -qO- https://herdr.dev/install.sh | sh || herdr_ok=0
    else
      herdr_ok=0
    fi
    if [ "${herdr_ok:-1}" = "0" ]; then
      echo "lasso: couldn't install herdr automatically — install it with:" >&2
      echo "  curl -fsSL https://herdr.dev/install.sh | sh" >&2
    fi
  fi
fi

echo
echo "next: run 'lasso start', then open http://127.0.0.1:8090"
