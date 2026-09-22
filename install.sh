#!/bin/sh
set -eu

repository="macguff/cx"
version="${CX_VERSION:-}"
install_dir="${CX_INSTALL_DIR:-${HOME}/.local/bin}"

if [ -z "$version" ]; then
    latest_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${repository}/releases/latest")"
    version="${latest_url##*/}"
fi
case "$version" in
    v*) ;;
    *) version="v${version}" ;;
esac

case "$(uname -s)" in
    Linux) os="linux" ;;
    Darwin) os="darwin" ;;
    *) echo "Unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

asset="cx_${version}_${os}_${arch}"
release_url="https://github.com/${repository}/releases/download/${version}"
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/cx-install.XXXXXX")"
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

echo "Downloading cx ${version} for ${os}/${arch}..."
curl -fsSL "${release_url}/${asset}" -o "${temp_dir}/${asset}"
curl -fsSL "${release_url}/checksums.txt" -o "${temp_dir}/checksums.txt"

expected="$(awk -v name="$asset" '{ file=$2; sub(/^.*\//, "", file); if (file == name) { print $1; exit } }' "${temp_dir}/checksums.txt")"
if [ -z "$expected" ]; then
    echo "Checksum for ${asset} was not found." >&2
    exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "${temp_dir}/${asset}" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "${temp_dir}/${asset}" | awk '{print $1}')"
else
    echo "sha256sum or shasum is required to verify the download." >&2
    exit 1
fi
if [ "$actual" != "$expected" ]; then
    echo "Checksum verification failed for ${asset}." >&2
    exit 1
fi

mkdir -p "$install_dir"
install -m 0755 "${temp_dir}/${asset}" "${install_dir}/cx"
echo "Installed cx ${version} to ${install_dir}/cx"
echo "Make sure ${install_dir} is on your PATH."
