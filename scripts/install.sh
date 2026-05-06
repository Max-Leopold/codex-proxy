#!/usr/bin/env bash
set -euo pipefail

repo="${REPO:-Max-Leopold/codex-proxy}"
binary="codex-proxy"
install_dir="${INSTALL_DIR:-${PREFIX:-/usr/local}/bin}"
version="${VERSION:-latest}"

err() {
  printf 'codex-proxy install: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || err "required command not found: $1"
}

need curl
need install

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) err "unsupported OS: $(uname -s)" ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch="amd64" ;;
  arm64 | aarch64) arch="arm64" ;;
  *) err "unsupported architecture: $(uname -m)" ;;
esac

asset="${binary}_${os}_${arch}"
url="https://github.com/${repo}/releases/download/${version}/${asset}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

printf 'Downloading %s\n' "${url}"
curl -fsSL "${url}" -o "${tmpdir}/${binary}"

if mkdir -p "${install_dir}" 2>/dev/null && [[ -w "${install_dir}" ]]; then
  install -m 0755 "${tmpdir}/${binary}" "${install_dir}/${binary}"
else
  need sudo
  sudo mkdir -p "${install_dir}"
  sudo install -m 0755 "${tmpdir}/${binary}" "${install_dir}/${binary}"
fi

printf 'Installed %s to %s\n' "${binary}" "${install_dir}/${binary}"
