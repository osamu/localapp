#!/bin/sh
# localapp installer.
#
#   curl -fsSL https://raw.githubusercontent.com/osamu/localapp/main/install.sh | sh
#
# Downloads the latest release binary for this machine, verifies its sha256
# checksum against the released checksums.txt, and installs it to
# /usr/local/bin (override with LOCALAPP_BIN_DIR). Pin a version with
# LOCALAPP_VERSION=v0.1.0. This script never runs `localapp install` for you:
# the one-time setup that touches the DNS resolver and the trust store stays
# an explicit, separate step.
set -eu

REPO="osamu/localapp"
BIN_DIR="${LOCALAPP_BIN_DIR:-/usr/local/bin}"

os=$(uname -s)
case "$os" in
  Darwin) os=darwin ;;
  *)
    echo "localapp: unsupported OS: $os (only macOS is supported for now)" >&2
    exit 1
    ;;
esac

arch=$(uname -m)
case "$arch" in
  arm64 | aarch64) arch=arm64 ;;
  x86_64) arch=amd64 ;;
  *)
    echo "localapp: unsupported architecture: $arch" >&2
    exit 1
    ;;
esac

version="${LOCALAPP_VERSION:-}"
if [ -z "$version" ]; then
  version=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
fi
if [ -z "$version" ]; then
  echo "localapp: could not determine the latest version" >&2
  exit 1
fi

asset="localapp_${version}_${os}_${arch}"
base="https://github.com/${REPO}/releases/download/${version}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading localapp ${version} (${os}/${arch})..." >&2
curl -fsSL -o "${tmp}/${asset}.tar.gz" "${base}/${asset}.tar.gz"
curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt"

echo "Verifying checksum..." >&2
(cd "$tmp" && grep " ${asset}.tar.gz\$" checksums.txt | shasum -a 256 -c -) >/dev/null

tar -C "$tmp" -xzf "${tmp}/${asset}.tar.gz"

if [ -d "$BIN_DIR" ] && [ -w "$BIN_DIR" ]; then
  install "${tmp}/${asset}/localapp" "${BIN_DIR}/localapp"
else
  echo "Installing to ${BIN_DIR} (sudo required)..." >&2
  sudo install -d "$BIN_DIR"
  sudo install "${tmp}/${asset}/localapp" "${BIN_DIR}/localapp"
fi

echo "Installed: ${BIN_DIR}/localapp ($("${BIN_DIR}/localapp" version))" >&2
echo "" >&2
echo "Next step (one-time setup, requires sudo):" >&2
echo "  sudo localapp install" >&2
