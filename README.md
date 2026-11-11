English | [简体中文](README.zh-CN.md)

<div align="center">

# Fleetty

**SSH-native terminal monitor for workstations, GPU servers, NAS nodes, and Slurm clusters.**

[![CI](https://img.shields.io/github/actions/workflow/status/Rhythmicc/Fleetty/ci.yml?branch=main&label=CI)](https://github.com/Rhythmicc/Fleetty/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Rhythmicc/Fleetty)](https://github.com/Rhythmicc/Fleetty/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Rhythmicc/Fleetty)](go.mod)
[![License](https://img.shields.io/github/license/Rhythmicc/Fleetty)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Rhythmicc/fleetty)](https://goreportcard.com/report/github.com/Rhythmicc/fleetty)

<img src="docs/images/fleetty-hero.webp" alt="Fleetty — terminal fleet monitor" width="100%">

</div>

**Fleetty** is a single-binary terminal console for monitoring local workstations, GPU compute nodes, storage servers, and multi-cluster Slurm queues. Run `fleetty top` locally without root privileges or open network ports, or run `fleetty serve` to publish an interactive dashboard on a dedicated SSH port (`23234` for a node, `23235` for a multi-server Hub).

The SSH listener serves only the Fleetty interface and never spawns a system shell. Client public keys, Hub RPC keys, and password-protected management actions are isolated from one another, while layout, sort order, filter state, and light/dark themes remain independent per session.

## Features

- **Host & Per-Core CPU Telemetry** — Live CPU utilization, per-core load bars, and clock frequencies (`/proc/cpuinfo` on Linux; rootless `IOReport` active-residency DVFS weighting on Apple Silicon), alongside memory, disk health, battery status, 7-day minute-resolution history (`history.jsonl`), and a sortable, filterable process table.
- **Hardware-Aware GPU Monitoring** — Supports NVIDIA GPUs on Linux (via `nvidia-smi`) and Apple Silicon GPUs on macOS (via `IOKit`, without root). Panels adapt to the host: if `nvidia-smi` fails on Linux and the PCI inventory reports no NVIDIA display or 3D controller, GPU regions hide automatically so neighboring panels reclaim the space; if an NVIDIA device is present, driver or permission errors stay visible.
- **Multi-Server Hub** — Aggregate fleet health on a single home screen (`nodes.json` on port `23235`), grouped by role (`gpu`, `cpu`, `network`, `process`). Inspect load, memory, temperature, bidirectional throughput, and host address at a glance, then press `Enter` to open any node over a persistent SSH RPC connection. Unreachable nodes display their last-seen status and retry with automatic backoff without stalling the 1-second refresh loop.
- **Multi-Cluster Slurm Queues** — Poll multiple independent Slurm controllers via `local` execution or a read-only `ssh` login-node transport (`sinfo` and `squeue`). View jobs allocated to a specific node, the earliest eligible pending candidate in each partition (`NEXT`), scheduling `WEIGHT`, and color-coded `QOS`. Narrow terminals (`< 112` columns) automatically switch to a compact table that preserves job ID, user, state, elapsed time, and job name.
- **NAS & Service Health Checks** — Configure `"profile": "nas"` (or `"storage"`) to front physical network interface rates, cumulative bytes, errors, and drops, mount-point capacity warnings, read-only Docker container telemetry, rootless-compatible PM2 process tables (`pm2_user`), and local HTTP health probes.
- **Local Storage Map & Safe Deduplication** — Browse disk allocation as an interactive treemap (`4`, local `fleetty top` only) backed by a concurrent scanner (`2`–`8` workers) and bounded session cache. Permanently delete entries (`d`), compress into a verified `.7z` archive before removal (`z`), or scan for duplicate files `>= 1 MiB` (`u`) using parallel SHA-256 hashing to generate inspectable symlink deduplication scripts under `fleetty/dedupe-plans`. Every destructive operation requires explicit phrase confirmation (`DELETE` or `ARCHIVE`); remote SSH sessions never receive file-modification access.
- **Zero-Shell SSH & Tiered Privilege Model** — Built on Wish and Bubble Tea. SSH usernames label the session rather than logging into a Unix account. Read-only process details redact tokens and passwords until authenticated via `ADMIN_PASSWORD_HASH` (bcrypt). An optional hardened helper (`fleetty-privileged.service` over `/run/fleetty/privileged.sock`) lets an unprivileged node service restart `fleetty.service`, reboot the host, or send `SIGTERM` to non-PID-1 processes with matching start times.
- **Headless Export & Staged Fleet Rollouts** — Emit JSON snapshots (`fleetty snapshot [--processes]`) or Prometheus metrics (`fleetty metrics`, `fleettyctl metrics`). Validate, plan, deploy, and update nodes with `fleettyctl`, including Hub-cached release rollouts cascaded through SSH login relays with automatic systemd rollback on failure.

## Screenshots

Captured from running Fleetty sessions.

The Hub organizes machines by group, preserves last-seen timestamps when a node drops offline, and reconnects automatically in the background.

<a href="docs/images/fleetty-hub.webp">
  <img src="docs/images/fleetty-hub.webp" alt="Fleetty Hub overview" width="100%">
</a>

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>GPU compute node</strong><br>
      CPU, memory, disk, network, GPU, the Slurm node queue, and processes on one screen.<br><br>
      <a href="docs/images/fleetty-gpu.webp">
        <img src="docs/images/fleetty-gpu.webp" alt="Fleetty GPU compute node">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>NAS and service node</strong><br>
      Network, mounts, Docker, PM2, and HTTP health, in front of the rest of the host.<br><br>
      <a href="docs/images/fleetty-nas.webp">
        <img src="docs/images/fleetty-nas.webp" alt="Fleetty NAS node">
      </a>
    </td>
  </tr>
</table>

## Installation

Local monitoring (`fleetty top`) and the fleet CLI (`fleettyctl`) run on **Linux** and **macOS** (`amd64` and `arm64`). Managed SSH node, Hub, and privileged-helper services (`fleetty install` and `fleetty doctor`) require **Linux with `systemd`** (`amd64` and `arm64`); foreground `fleetty serve` runs without `systemd`.

### Install script

```bash
curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
```

### Install with Go

Requires the Go version declared in [go.mod](go.mod):

```bash
go install github.com/Rhythmicc/fleetty/cmd/fleetty@latest
go install github.com/Rhythmicc/fleetty/cmd/fleettyctl@latest
```

### Prebuilt binaries (GitHub Releases)

Prebuilt binaries (`fleetty_linux_amd64`, `fleetty_linux_arm64`, `fleetty_darwin_amd64`, `fleetty_darwin_arm64`, matching `fleettyctl_*` binaries, systemd units, example JSON manifests, Ghostty themes, and `checksums.txt`) are published on [GitHub Releases](https://github.com/Rhythmicc/Fleetty/releases/latest). To download the latest release, verify its SHA-256 checksum, and install it to `~/.local/bin/fleetty`:

```bash
case "$(uname -s):$(uname -m)" in
  Linux:x86_64)  fleetty_asset=fleetty_linux_amd64 ;;
  Linux:aarch64|Linux:arm64) fleetty_asset=fleetty_linux_arm64 ;;
  Darwin:x86_64) fleetty_asset=fleetty_darwin_amd64 ;;
  Darwin:arm64)  fleetty_asset=fleetty_darwin_arm64 ;;
  *) echo "Unsupported platform" >&2; exit 1 ;;
esac

fleetty_download_dir="$(mktemp -d)"
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fL "$fleetty_release_base/$fleetty_asset" -o "$fleetty_download_dir/$fleetty_asset" &&
curl -fL "$fleetty_release_base/checksums.txt" -o "$fleetty_download_dir/checksums.txt" &&
fleetty_expected="$(awk -v asset="$fleetty_asset" '$2 == asset { print $1 }' "$fleetty_download_dir/checksums.txt")" &&
if command -v sha256sum >/dev/null 2>&1; then
  fleetty_actual="$(sha256sum "$fleetty_download_dir/$fleetty_asset" | awk '{ print $1 }')"
else
  fleetty_actual="$(shasum -a 256 "$fleetty_download_dir/$fleetty_asset" | awk '{ print $1 }')"
fi &&
test -n "$fleetty_expected" && test "$fleetty_expected" = "$fleetty_actual" &&
mkdir -p "$HOME/.local/bin" &&
install -m 0755 "$fleetty_download_dir/$fleetty_asset" "$HOME/.local/bin/fleetty"
```

## Quick start

### 1. Local monitoring

Local monitoring does not require root and does not open a listening socket:

```bash
"$HOME/.local/bin/fleetty" top
```

Once `~/.local/bin` is on your `PATH`, run `fleetty top` directly (optionally passing `--config PATH`, `--theme auto|dark|light`, or `--layout PATH`). Running `fleetty` with no arguments opens the interactive launcher; the SSH server starts only when you run `fleetty serve` or select **SSH Server** in the launcher.

### 2. Remote SSH node and multi-server Hub

On a Linux server, stage the authorized client public keys, run the atomic installer, and verify the service as described in [Install a Linux node](docs/en/installation.md#linux-ssh-nodes):

```bash
fleetty_config="$(mktemp -d)"
chmod 0700 "${fleetty_config}"
install -m 0600 ./client.pub "${fleetty_config}/authorized_keys"

"$HOME/.local/bin/fleetty" install --role node --scope user --config-dir "${fleetty_config}"
"$HOME/.local/bin/fleetty" doctor --role node --scope user
```

Then connect from any SSH client:

```bash
ssh -p 23234 monitor@node-host    # one node
ssh -p 23235 monitor@hub-host     # a Hub of many servers
```

The SSH username only labels the session; it never logs into a system account, and only public keys listed in `authorized_keys` are accepted. Read-only monitoring requires no password, while administrative actions are authenticated separately.

- [Install a Linux node, as a user or as a system service](docs/en/installation.md#linux-ssh-nodes)
- [Configure a multi-server Hub](docs/en/hub.md)
- [Attach local or SSH-relayed Slurm queues](docs/en/slurm.md)
- [Roll out updates from a Hub, including compute nodes behind a login node](docs/en/deployment.md#updating-nodes-from-a-hub)

### 3. Headless snapshots and Prometheus metrics

Export a one-shot JSON snapshot (including the last 60 minutes of history) or Prometheus exposition text locally or across a fleet manifest:

```bash
fleetty snapshot --processes
fleetty metrics --config /etc/fleetty/machine.json
fleettyctl metrics --file fleet.json
```

## Keybindings

### Node monitor and local `top`

| Key | Action |
| --- | --- |
| `1` / `2` / `3` | Switch to **Overview** / **Compute** / **Network** |
| `4` | Switch to local **Storage Map** (`fleetty top` only) |
| `Tab` / `Shift+Tab` | Next / previous page |
| `↑` / `↓`, click | Select a process, node, or directory entry |
| `Enter` / `Esc` | Open details or directory / return or quit |
| `/` / `c` | Filter processes / clear filter (or cycle kept file in duplicate view) |
| `o` / `O`, click header | Next sort field (`CPU`, `MEM%`, `RSS`, `ELAPSED`, `PID`, `USER`, `COMMAND`) / reverse order |
| `f` / `v` | Focus a CPU core / toggle compact column chart vs. full per-core list (`> 32` logical CPUs) |
| `d` / `z` / `u` / `g` / `G` | Delete (`DELETE`) / `.7z` archive (`ARCHIVE`) / duplicate scan / write group or batch dedupe script |
| `l` | Open custom widget layout editor (`K`/`J` move, `-`/`+` resize, `Space` hide/show, `s` save locally) |
| `m` | Enter management mode (when `ADMIN_PASSWORD_HASH` is configured) |
| `t` | Toggle session theme (`dark` / `light`) and switch from `auto` to manual for this session |
| `r` / `q` | Refresh immediately / quit |

### Multi-server Hub and Slurm view

| Key | Action |
| --- | --- |
| `↑` / `↓` / `←` / `→`, click | Select a server card |
| `Enter` | Open the selected server's monitor |
| `s` / `Tab` | Open the Slurm queue / switch between servers and Slurm view |
| `←` / `→` / `a` | On the Slurm page, filter by cluster / show all clusters |
| `r` / `t` / `Esc` / `q` | Refresh all nodes and queues / toggle session theme / return or quit |

- **Adaptive CPU view**: On **Compute**, machines with 32 or fewer logical CPUs render one horizontal bar per core. Above 32 logical CPUs, Fleetty switches to a compact two-row vertical column chart sized to the terminal width with CPU history placed in a narrow right-hand column.
- **Accurate network attribution**: **Network** displays live throughput and cumulative traffic. On Linux, active TCP connections are sampled via `INET_DIAG` / `TCP_INFO`, ordered descending by live `DOWN + UP`, and attributed to the socket's owning user; unavailable or unsampled counters display `--` rather than a synthetic zero. On macOS, per-process traffic is collected via `nettop`.
- **Local-only storage writes**: **Storage Map** is restricted to local `fleetty top`. Delete, `.7z` archive, and deduplication script generation each require explicit confirmation, skip network and virtual mounts, and never expose write capabilities over remote SSH sessions.

[Using Fleetty](docs/en/usage.md) · [Authentication and management](docs/en/security.md)

## Ghostty and themes

We recommend [Ghostty](https://ghostty.org/) for both local and SSH sessions. The repository ships [Fleetty Dark](themes/ghostty/Fleetty%20Dark) (charcoal background with balanced bright accents) and [Fleetty Light](themes/ghostty/Fleetty%20Light) (warm paper background with a solid black cursor).

Install both themes on the **client** machine and set Ghostty's `theme`:

```ini
theme = dark:Fleetty Dark,light:Fleetty Light
```

Ghostty tracks the operating system's light/dark appearance, and Fleetty's default `auto` theme (`--theme auto` or `DEFAULT_THEME=auto`) queries the client terminal and follows appearance changes live—both locally and over direct SSH connections—without forwarding environment variables. Pressing `t` overrides `auto` and pins the theme manually for the current session.

See [Ghostty and themes](docs/en/themes.md) for theme installation one-liners and matching Powerlevel10k prompt colors. Setting `background-opacity = 1` in Ghostty is recommended to keep contrast consistent; fonts, keybindings, and window settings remain untouched.

## Configuration and paths

`fleetty install` supports both unprivileged `user` scope (default for non-root accounts) and `system` scope (default for `root`):

| Item | User scope (`--scope user`) | System scope (`--scope system`) |
| --- | --- | --- |
| Binary | `~/.local/bin/fleetty` | `/opt/fleetty/fleetty` |
| Configuration directory (`0700`) | `~/.config/fleetty` | `/etc/fleetty` |
| systemd unit directory | `~/.config/systemd/user` | `/etc/systemd/system` |
| Node / Hub unit names | `fleetty.service` / `fleetty-hub.service` | `fleetty.service` / `fleetty-hub.service` |
| SSH host keys | `ssh_host_ed25519` / `hub_host_ed25519` | `ssh_host_ed25519` / `hub_host_ed25519` |
| Node / Hub config files | `machine.json` / `nodes.json` | `machine.json` / `nodes.json` |
| Client / Hub RPC authorized keys | `authorized_keys` / `hub_authorized_keys` | `authorized_keys` / `hub_authorized_keys` |
| Management credentials | `admin.env` (`ADMIN_PASSWORD_HASH`) | `admin.env` (`ADMIN_PASSWORD_HASH`) |

Key environment variables for `fleetty serve` include `SSH_HOST` (`0.0.0.0`), `SSH_PORT` (`23234` for a node, `23235` for a Hub), `SSH_HOST_KEY_PATH`, `SSH_AUTHORIZED_KEYS_FILE`, `NODE_RPC_AUTHORIZED_KEYS_FILE`, `MACHINE_CONFIG_FILE`, `HUB_NODES_FILE`, `FLEETTY_HISTORY_FILE`, `FLEETTY_LAYOUT_FILE`, `DEFAULT_THEME` (`auto` | `dark` | `light`), `ADMIN_PASSWORD_HASH`, and `FLEETTY_PRIVILEGED_SOCKET` (`/run/fleetty/privileged.sock`). Full reference and troubleshooting steps are in [Configuration and troubleshooting](docs/en/configuration.md).

## Updates and fleet rollout

Update a single binary in place with SHA-256 verification and automatic pre-flight version checking:

```bash
fleetty update --check             # report only
fleetty update                     # latest stable release
fleetty update --version v0.1.4    # a specific tag
```

`fleetty update` atomically replaces the executed binary and keeps the previous executable at a backup path. It never modifies configuration or SSH keys and does not restart running services automatically—restart the corresponding `systemd` unit when ready, and clients will pick up the new version on their next connection.

For multi-machine deployments and staged upgrades across SSH login relays, use `fleettyctl`:

```bash
fleettyctl validate --file fleet.json
fleettyctl plan     --file fleet.json
fleettyctl apply    --file fleet.json --yes
fleettyctl update   --file /etc/fleetty/fleet-update.json --yes
```

With `fleettyctl update`, the Hub downloads and verifies each release archive once, caches it locally, and distributes it across direct nodes and nested `relay` login nodes.

[Install and upgrade one machine](docs/en/installation.md) · [Fleet rollout and scheduled updates](docs/en/deployment.md)

## Documentation

| Topic | Guide |
| --- | --- |
| System requirements, installation, self-update, uninstall | [Install and upgrade](docs/en/installation.md) |
| Pages, per-core CPU, clock frequencies, network, storage map | [Using Fleetty](docs/en/usage.md) |
| Terminal themes, automatic appearance following, prompt colors | [Ghostty and themes](docs/en/themes.md) |
| Multi-server aggregation and Hub-to-node RPC authentication | [Hub](docs/en/hub.md) |
| Multi-cluster polling, job states, QOS, compact narrow queues | [Slurm](docs/en/slurm.md) |
| Physical NICs, mount alerts, Docker, PM2, HTTP health checks | [NAS and service checks](docs/en/nas.md) |
| Idempotent installer, fleet manifests, cascaded Hub updates | [Deployment](docs/en/deployment.md) |
| Management password hashes, SSH keys, privileged helper socket | [Authentication and management](docs/en/security.md) |
| Environment settings, systemd logs, common troubleshooting | [Configuration](docs/en/configuration.md) |
| Sampling intervals, SSH connection reuse, retained JSONL history | [Collection and performance](docs/en/performance.md) |

## Build from source

Use the Go version declared in [go.mod](go.mod).

```bash
go test -race ./...
go vet ./...
go build -o fleetty ./cmd/fleetty
go build -o fleettyctl ./cmd/fleettyctl
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and pull requests are welcome. When changing a CLI flag, configuration key, or user-visible behavior, please update both the English (`docs/en/`) and Simplified Chinese (`docs/zh-CN/`) documentation in the same pull request.

## License

[MIT](LICENSE)
