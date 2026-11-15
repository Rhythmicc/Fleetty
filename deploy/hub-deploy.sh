#!/usr/bin/env bash
set -euo pipefail

# Replace only the binary of an existing system Hub. The general installer
# also replaces the unit, which is not wanted for these development deploys.
usage() {
    printf 'Usage: bash deploy/hub-deploy.sh [--check] SSH_TARGET [SSH_OPTIONS...]\n'
}

fail() { printf '%s\n' "$*" >&2; exit 1; }
quote() { printf "'%s'" "${1//\'/\'\\\'\'}"; }

sha256() {
    if command -v sha256sum >/dev/null; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

remote_probe() {
    set -euo pipefail
    systemctl is-active --quiet fleetty-hub.service
    systemctl show fleetty-hub.service -p ExecStart --value |
        grep -F 'path=/opt/fleetty/fleetty ;' >/dev/null
    [[ -f /opt/fleetty/fleetty && ! -L /opt/fleetty/fleetty ]]
    # Refuse to replace an executable that is also in use by a node service.
    if systemctl is-active --quiet fleetty.service || systemctl is-enabled --quiet fleetty.service; then
        if systemctl show fleetty.service -p ExecStart --value |
            grep -F 'path=/opt/fleetty/fleetty ;' >/dev/null; then
            printf 'Hub shares its executable with an active or enabled node service.\n' >&2
            return 1
        fi
    fi
    command -v zstd >/dev/null
    command -v flock >/dev/null
    printf '%s %s\n' "$(uname -m)" "$(sha256sum /opt/fleetty/fleetty | awk '{print $1}')"
}

remote_stage() {
    set -euo pipefail
    umask 077
    stage=$(mktemp -d /tmp/fleetty-hub-deploy.XXXXXXXX)
    trap 'code=$?; if [ "$code" -ne 0 ]; then rm -f -- "$stage/fleetty"; rmdir -- "$stage"; fi' EXIT
    if [ "$1" = delta ]; then
        printf '%s  /opt/fleetty/fleetty\n' "$2" | sha256sum --status -c -
        zstd -q -d --patch-from=/opt/fleetty/fleetty - -o "$stage/fleetty"
    else
        zstd -q -d - -o "$stage/fleetty"
    fi
    printf '%s  %s\n' "$3" "$stage/fleetty" | sha256sum --status -c -
    printf '%s\n' "$stage/fleetty"
}

remote_install() {
    set -euo pipefail
    umask 077
    candidate=$1 expected=$2 previous=$3
    binary=/opt/fleetty/fleetty
    unit=fleetty-hub.service
    case "$candidate" in /tmp/fleetty-hub-deploy.????????/fleetty) ;; *) return 2;; esac
    [[ -f "$candidate" && ! -L "$candidate" ]]
    [[ -f "$binary" && ! -L "$binary" ]]
    exec 9>"$binary.update.lock"
    flock -n 9 || { printf 'Another Fleetty update is in progress.\n' >&2; return 1; }

    healthy() {
        pid=$(systemctl show "$unit" -p MainPID --value) &&
            test "$pid" -gt 0 &&
            systemctl is-active --quiet "$unit" &&
            printf '%s  %s\n' "$1" "/proc/$pid/exe" | sha256sum --status -c - &&
            ss -H -ltnp | grep -F "pid=$pid," >/dev/null
    }
    healthy "$previous"
    old_pid=$pid
    printf '%s  %s\n' "$previous" "$binary" | sha256sum --status -c -
    if [ "$previous" = "$expected" ]; then
        printf 'Hub already runs this binary.\n'
        return
    fi
    backup=$(mktemp -d /var/backups/fleetty-hub-deploy.XXXXXXXX)
    cp -a -- "$binary" "$backup/fleetty"
    printf 'Rollback backup: %s/fleetty\n' "$backup"
    next=$(mktemp /opt/fleetty/.fleetty-hub-next.XXXXXXXX)
    replaced=0
    finish() {
        code=$?
        trap - EXIT HUP INT TERM
        if [ "$replaced" = 1 ]; then
            printf 'New Hub failed validation; restoring the previous binary.\n' >&2
            if install -o root -g root -m 0755 "$backup/fleetty" "$next" &&
                mv -fT -- "$next" "$binary" &&
                systemctl restart "$unit" &&
                wait_healthy "$previous"; then
                printf 'Rollback verified.\n' >&2
            else
                printf 'Rollback failed; backup is %s/fleetty\n' "$backup" >&2
            fi
        fi
        rm -f -- "$next"
        exit "$code"
    }
    wait_healthy() {
        tries=0
        until healthy "$1" && [ "$pid" != "$old_pid" ]; do
            tries=$((tries + 1))
            [ "$tries" -lt 30 ] || return 1
            sleep 0.5
        done
        stable_pid=$pid
        sleep 2
        healthy "$1" && [ "$pid" = "$stable_pid" ]
    }
    trap finish EXIT
    trap 'exit 1' HUP INT TERM
    install -o root -g root -m 0755 "$candidate" "$next"
    printf '%s  %s\n' "$expected" "$next" | sha256sum -c -
    "$next" version --require-capability hub-groups-v1 --require-capability process-table-v2 \
        --require-capability terminal-footer-v1 --json
    replaced=1
    mv -fT -- "$next" "$binary"
    systemctl restart "$unit"
    wait_healthy "$expected"
    replaced=0
    printf 'Hub deployed and listening (PID %s).\n' "$pid"
}

remote_command() {
    local name=$1 code arg
    shift
    code=$(declare -f "$name"; printf '\n%s "$@"\n' "$name")
    printf 'bash -c %s fleetty-hub-deploy' "$(quote "$code")"
    for arg in "$@"; do
        printf ' %s' "$(quote "$arg")"
    done
}

check_only=0
case "${1:-}" in
    -h|--help) usage; exit 0;;
    --check) check_only=1; shift;;
esac
[[ "${1:-}" =~ ^[[:alnum:]][[:alnum:]_.@:-]*$ ]] || { usage >&2; exit 2; }
target=$1
shift
ssh_args=(-o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=12
    -o ServerAliveInterval=15 -o ServerAliveCountMax=3 "$@")
probe=$(ssh -T "${ssh_args[@]}" "$target" "$(remote_command remote_probe)")
read -r machine previous extra <<< "$probe"
[[ "$previous" =~ ^[a-f0-9]{64}$ && -z "$extra" ]] || fail "Invalid Hub preflight response."
case "$machine" in
    x86_64) arch=amd64;;
    aarch64|arm64) arch=arm64;;
    *) fail "Unsupported Hub architecture: $machine";;
esac
printf 'Hub preflight OK: %s, linux/%s, current SHA-256 %s\n' "$target" "$arch" "$previous"
if [ "$check_only" = 1 ]; then exit 0; fi

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
command -v zstd >/dev/null || fail "zstd is required on the deployment machine."
cache_dir=${FLEETTY_HUB_DEPLOY_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/fleetty/hub-deploy}
umask 077
mkdir -p -- "$cache_dir"
work=$(mktemp -d "$cache_dir/staging.XXXXXXXX")
trap 'rm -f -- "$work/fleetty" "$work/payload.zst"; rmdir -- "$work"' EXIT
base_tag=$(git describe --tags --abbrev=0 --match 'v[0-9]*')
commit=$(git rev-parse HEAD)
date_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
version="${base_tag%%+*}+hub.$(date -u +%Y%m%d%H%M%S)"
buildinfo=github.com/Rhythmicc/fleetty/internal/buildinfo
printf 'Building %s...\n' "$version"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags="-s -w -X $buildinfo.Version=$version -X $buildinfo.Commit=$commit -X $buildinfo.BuildDate=$date_utc" \
    -o "$work/fleetty" ./cmd/fleetty
expected=$(sha256 "$work/fleetty")
mode=full
if [ -f "$cache_dir/$previous" ] && [ "$(sha256 "$cache_dir/$previous")" = "$previous" ]; then
    mode=delta
    zstd -q -19 --patch-from="$cache_dir/$previous" "$work/fleetty" -o "$work/payload.zst"
else
    zstd -q -3 "$work/fleetty" -o "$work/payload.zst"
fi
printf 'Uploading %s payload (%s bytes)...\n' "$mode" "$(wc -c < "$work/payload.zst" | tr -d '[:space:]')"
candidate=$(ssh -T "${ssh_args[@]}" "$target" "$(remote_command remote_stage "$mode" "$previous" "$expected")" < "$work/payload.zst")
[[ "$candidate" =~ ^/tmp/fleetty-hub-deploy\.[[:alnum:]]{8}/fleetty$ ]] || fail "Invalid Hub staging path."
install_command=$(remote_command remote_install "$candidate" "$expected" "$previous")
ssh -tt "${ssh_args[@]}" "$target" \
    "sudo -- $install_command; code=\$?; rm -f -- $(quote "$candidate"); rmdir -- $(quote "${candidate%/*}"); exit \$code"
mv -f -- "$work/fleetty" "$cache_dir/$expected"
