English | [简体中文](../zh-CN/deployment.md)

[Back to the README](../../README.md)

# Deployment and fleet updates

Fleetty ships an installer that can be run again safely. `--scope auto` selects `user` for a normal account and `system` for root. One machine:

```bash
./fleetty_linux_amd64 install \
  --role node \
  --scope user \
  --config-dir ./node-config
~/.local/bin/fleetty doctor --role node --scope user
```

A Hub uses `--role hub`. The installer writes the binary, the configuration, and the systemd unit for that scope by atomic replace. Unchanged files do not restart the service. If the service fails to start, the binary, the unit, and this run's configuration changes roll back.

Configuration files are owned by the service account and mode `0600`. The installer rejects symlinks, subdirectories, hidden files, and files over its size limit. An existing file that is not in the staged directory is left alone.

Several machines are described by `fleettyctl` and a JSON fleet manifest. The control machine can be Linux or macOS. SSH uses the local OpenSSH config, including proxies and host-key checking:

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
fleettyctl_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64) fleettyctl_arch=amd64 ;;
  arm64|aarch64) fleettyctl_arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
fleettyctl_asset="fleettyctl_${fleettyctl_os}_${fleettyctl_arch}"
curl -fL \
  -o "./${fleettyctl_asset}" \
  "${fleetty_release_base}/${fleettyctl_asset}"
curl -fL \
  -o ./fleetty-checksums.txt \
  "${fleetty_release_base}/checksums.txt"
fleettyctl_expected="$(
  awk -v asset="${fleettyctl_asset}" '$2 == asset { print $1 }' \
    ./fleetty-checksums.txt
)"
if [ "${fleettyctl_os}" = "darwin" ]; then
  fleettyctl_actual="$(shasum -a 256 "./${fleettyctl_asset}" | awk '{ print $1 }')"
else
  fleettyctl_actual="$(sha256sum "./${fleettyctl_asset}" | awk '{ print $1 }')"
fi
test -n "${fleettyctl_expected}"
test "${fleettyctl_expected}" = "${fleettyctl_actual}"
mv "./${fleettyctl_asset}" ./fleettyctl
chmod 0755 ./fleettyctl

fleettyctl validate --file fleet.json
fleettyctl plan --file fleet.json
fleettyctl apply --file fleet.json --yes
fleettyctl status --file fleet.json
```

`fleettyctl status --json` reads one live metrics snapshot on each target, in addition to the deploy state, and merges it into a `metrics` field for scripts and external monitoring. For Prometheus text:

```bash
fleettyctl metrics --file fleet.json
fleettyctl metrics --file fleet.json --target gpu-1
```

`metrics` reuses each target's SSH channel from the manifest and runs `fleetty metrics` there. It opens no extra port and does not need the node RPC key. The text includes CPU, memory, disk, network, GPU, mounts, service health, and Docker/PM2 counts. `--json` returns one machine-readable result per target. On the node itself, `fleetty snapshot` (optionally `--processes`) or `fleetty metrics --config machine.json` prints the same kind of data, for example into node_exporter's textfile collector. The snapshot's `history` field is the last 60 minutes, not the full on-disk JSONL. See [Collection and performance](performance.md).

Example:

```json
{
  "version": 1,
  "binary": "./fleetty_linux_amd64",
  "parallel": 4,
  "timeout_seconds": 15,
  "targets": [
    {
      "name": "gpu-1",
      "ssh": "gpu-1-admin",
      "role": "node",
      "scope": "user",
      "config_dir": "./config/gpu-1"
    },
    {
      "name": "lab-hub",
      "ssh": "hub-admin",
      "role": "hub",
      "scope": "system",
      "config_dir": "./config/hub"
    }
  ]
}
```

`binary` and `config_dir` are resolved relative to the manifest's directory. A target may set its own `binary` and override the global one. `role` is `node`, `hub`, or `privileged-helper`. The helper is system scope only. `scope: "user"` defaults to `become: "none"` and never calls sudo. `scope: "system"` defaults to `become: "sudo"`. A root SSH session can set `"become": "none"` explicitly.

The manifest has no password field. OpenSSH holds the SSH credentials. Fleetty's management password, RPC private keys, and anything else secret belong in the target's `config_dir`, readable only by the account that deploys. `plan` compares SHA-256 of the binary and the managed configuration, plus whether the service is enabled and running. `apply` touches only targets that differ. The remote install is still the same atomic installer. `apply` refuses to run without `--yes`.

Only a system-scope deploy account is equivalent to a root administrator. `become: "sudo"` requires non-interactive administrative sudo. It is not a low-privilege grant. User scope manages that SSH user's own Fleetty files and user service, and it is the right default.

## Updating nodes from a Hub

The Hub can be the single place that downloads and distributes Fleetty archives. Each round downloads a release asset once, checks SHA-256, writes it to a local cache, and walks the adjacency declared in the update manifest. The Hub updates nodes it can reach directly. A compute node behind a login node is updated by that login node. The far end still uses Fleetty's own installer and rolls back if the service does not come up. A machine that is off, or unreachable for now, is marked `DEFERRED`. Only its branch pauses. Other nodes continue, and the deferred branch is tried again next round.

On the Hub, prepare a deploy SSH key and OpenSSH host-key checking, then install `fleettyctl`:

```bash
sudo install -d -o root -g root -m 0755 /opt/fleetty
sudo install -o root -g root -m 0755 \
  fleettyctl_linux_amd64 /opt/fleetty/fleettyctl
sudo install -o root -g root -m 0600 \
  fleet-update.example.json /etc/fleetty/fleet-update.json
```

The update manifest has no `binary` field. It names the release to download:

```json
{
  "version": 1,
  "release": {
    "base_url": "https://github.com/Rhythmicc/Fleetty/releases/latest/download",
    "cache_dir": "/var/cache/fleetty/releases"
  },
  "parallel": 3,
  "timeout_seconds": 15,
  "targets": [
    {
      "name": "gpu-login",
      "ssh": "gpu-login",
      "role": "relay",
      "arch": "amd64",
      "children": [
        {
          "name": "gpu-1",
          "ssh": "gpu-1-admin",
          "role": "node",
          "scope": "system",
          "become": "sudo",
          "arch": "amd64"
        }
      ]
    },
    {
      "name": "storage-1",
      "ssh": "storage-1-admin",
      "role": "node",
      "scope": "user",
      "arch": "amd64"
    }
  ]
}
```

`ssh` is interpreted by the target's parent. A top-level entry is an OpenSSH alias on the Hub. An alias inside `children` only has to work on the `relay` login node that owns it. The Hub does not need a route, a private key, or a `known_hosts` entry for a leaf. A `relay` does not install or restart a service. Its parent sends a verified temporary `fleettyctl`, the subtree manifest, and the binaries that subtree needs. The relay plans or updates its neighbors and then deletes those files. Private keys and the SSH agent are not forwarded with the archives.

A `relay` may contain another `relay`. Depth is at most 8, and the whole tree is at most 512 targets. A nested target must set `arch`, so the Hub can pack a complete offline bundle without probing the leaf. Only a top-level target that the Hub dials directly may omit `arch`; the Hub then runs `uname -m`. Every hop should use `StrictHostKeyChecking` and its own deploy key. System scope requires non-interactive `sudo -n` on the neighboring deploy account. User scope does not call sudo. The monitoring RPC key is not reused for software updates, so a read-only monitor key cannot become a system administrator.

You only run `fleettyctl update` on the Hub. The `cascade` subcommand is invoked by the neighboring relay. It requires a matching SHA-256 on every temporary binary in the manifest, and it does not need its own timer.

Inspect the plan, then apply it:

```bash
sudo /opt/fleetty/fleettyctl update \
  --file /etc/fleetty/fleet-update.json
sudo /opt/fleetty/fleettyctl update \
  --file /etc/fleetty/fleet-update.json --yes
```

To run that on a schedule, install the systemd unit and timer shipped with the release. The timer fires 15 minutes after boot, then every 6 hours, with a random delay so many Hubs do not hit the download host together:

```bash
sudo install -o root -g root -m 0644 \
  fleetty-update.service fleetty-update.timer \
  /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now fleetty-update.timer
systemctl list-timers fleetty-update.timer
```
