#!/bin/sh
# Install Fleetty for the current user.
#
#   curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
#
# Environment:
#   FLEETTY_VERSION      release tag, or "latest" (default: latest)
#   FLEETTY_INSTALL_DIR  directory for the fleetty binary (default: ~/.local/bin)
#
# The script downloads the asset published by .github/workflows/release.yml
# (fleetty_linux_amd64, fleetty_linux_arm64, fleetty_darwin_amd64,
# fleetty_darwin_arm64), checks it against checksums.txt, and installs it.

set -eu

fleetty_repo="https://github.com/Rhythmicc/Fleetty"

die() {
  printf 'install.sh: %s\n' "$1" >&2
  exit 1
}

tmpdir=
stage=
cleanup() {
  if [ -n "${stage}" ] && [ -f "${stage}" ]; then
    rm -f "${stage}"
  fi
  if [ -n "${tmpdir}" ] && [ -d "${tmpdir}" ]; then
    rm -rf "${tmpdir}"
  fi
  tmpdir=
  stage=
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

if ! command -v curl >/dev/null 2>&1; then
  die "curl is required"
fi
if ! command -v awk >/dev/null 2>&1; then
  die "awk is required"
fi
if ! command -v mktemp >/dev/null 2>&1; then
  die "mktemp is required"
fi

version=${FLEETTY_VERSION:-latest}
printf '%s\n' "${version}" | awk '
  /^latest$/ { ok = 1 }
  /^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$/ { ok = 1 }
  END { exit(ok ? 0 : 1) }
' || die "FLEETTY_VERSION must be latest or a tag such as v1.2.3"

if [ -n "${FLEETTY_INSTALL_DIR:-}" ]; then
  install_dir=${FLEETTY_INSTALL_DIR}
else
  if [ -z "${HOME:-}" ]; then
    die "HOME is not set; set FLEETTY_INSTALL_DIR"
  fi
  install_dir="${HOME}/.local/bin"
fi
if [ "${install_dir}" != "/" ]; then
  install_dir=${install_dir%/}
fi
if [ -z "${install_dir}" ]; then
  die "FLEETTY_INSTALL_DIR is empty"
fi
case "${install_dir}" in
  *[![:print:]]*)
    die "FLEETTY_INSTALL_DIR contains a non-printable character"
    ;;
esac

os_name=$(uname -s)
arch_name=$(uname -m)
case "${os_name}" in
  Linux) goos=linux ;;
  Darwin) goos=darwin ;;
  *) die "unsupported operating system ${os_name} (Linux and Darwin only)" ;;
esac
case "${arch_name}" in
  x86_64 | amd64) goarch=amd64 ;;
  aarch64 | arm64) goarch=arm64 ;;
  *) die "unsupported architecture ${arch_name} (x86_64 and arm64 only)" ;;
esac

asset="fleetty_${goos}_${goarch}"
if [ "${version}" = "latest" ]; then
  base="${fleetty_repo}/releases/latest/download"
else
  base="${fleetty_repo}/releases/download/${version}"
fi

tmpdir=$(mktemp -d "${TMPDIR:-/tmp}/fleetty-install.XXXXXX")

fetch() {
  url=$1
  dest=$2
  label=$3
  if ! curl -fsSL --retry 3 --retry-delay 1 -A fleetty-install "${url}" -o "${dest}"; then
    die "could not download ${label} from ${url}"
  fi
}

fetch "${base}/checksums.txt" "${tmpdir}/checksums.txt" "checksums.txt"
fetch "${base}/${asset}" "${tmpdir}/${asset}" "${asset}"

expected=$(awk -v asset="${asset}" '
  function fail(message) {
    print message > "/dev/stderr"
    exit 2
  }
  {
    sub(/\r$/, "")
    if ($0 ~ /^[[:space:]]*$/) {
      fail("checksums.txt contains a blank line")
    }
    if (NF != 2) {
      fail("checksums.txt has a line that is not \"<sha256> <name>\"")
    }
    name = $2
    sub(/^\*/, "", name)
    if (name == "" || name ~ /[\/\\]/) {
      fail("checksums.txt has an invalid asset name")
    }
    if ($1 !~ /^[0-9A-Fa-f]{64}$/) {
      fail("checksums.txt has an invalid sha256")
    }
    if (name == asset) {
      if (found) {
        fail("checksums.txt lists " asset " more than once")
      }
      found = 1
      hash = tolower($1)
    }
  }
  END {
    if (!found) {
      fail("checksums.txt has no entry for " asset)
    }
    print hash
  }
' "${tmpdir}/checksums.txt") || die "could not read the checksum for ${asset}"

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "${tmpdir}/${asset}" > "${tmpdir}/actual.txt" || die "sha256sum failed"
elif command -v shasum >/dev/null 2>&1; then
  shasum -a 256 "${tmpdir}/${asset}" > "${tmpdir}/actual.txt" || die "shasum failed"
else
  die "sha256sum or shasum is required to verify the download"
fi
actual=$(awk 'NR == 1 { print tolower($1); exit }' "${tmpdir}/actual.txt")
case "${actual}" in
  *[!0-9a-f]* | "")
    die "could not hash ${asset}"
    ;;
esac
if [ "${#actual}" -ne 64 ]; then
  die "could not hash ${asset}"
fi
if [ "${expected}" != "${actual}" ]; then
  die "checksum mismatch for ${asset}"
fi

if ! mkdir -p "${install_dir}"; then
  die "could not create ${install_dir}"
fi
stage="${install_dir}/.fleetty-install.$$"
if ! cp "${tmpdir}/${asset}" "${stage}"; then
  die "could not write ${install_dir}/fleetty"
fi
if ! chmod 0755 "${stage}"; then
  die "could not mark ${install_dir}/fleetty executable"
fi
dest="${install_dir}/fleetty"
if ! mv -f "${stage}" "${dest}"; then
  die "could not install ${dest}"
fi
stage=

if ! "${dest}" version >/dev/null 2>&1; then
  die "installed ${dest}, but it failed to run"
fi

printf 'Installed %s\n' "${dest}"

if ! awk -v dir="${install_dir}" 'BEGIN {
  n = split(ENVIRON["PATH"], parts, ":")
  for (i = 1; i <= n; i++) {
    entry = parts[i]
    if (entry != "/" && substr(entry, length(entry), 1) == "/") {
      entry = substr(entry, 1, length(entry) - 1)
    }
    if (entry == dir) {
      exit 0
    }
  }
  exit 1
}'; then
  printf 'install.sh: %s is not on PATH.\n' "${install_dir}" >&2
  printf 'Add it for this shell with:\n' >&2
  printf '  PATH=%s:%s\n' "${install_dir}" "${PATH}" >&2
  printf '  export PATH\n' >&2
fi
