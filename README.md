English | [简体中文](README.zh-CN.md)

<div align="center">

# Fleetty

One machine or a whole fleet, from the terminal.

[![CI](https://img.shields.io/github/actions/workflow/status/Rhythmicc/Fleetty/ci.yml?branch=main&label=CI)](https://github.com/Rhythmicc/Fleetty/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Rhythmicc/Fleetty)](https://github.com/Rhythmicc/Fleetty/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Rhythmicc/Fleetty)](go.mod)
[![License](https://img.shields.io/github/license/Rhythmicc/Fleetty)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Rhythmicc/fleetty)](https://goreportcard.com/report/github.com/Rhythmicc/fleetty)

<img src="docs/images/fleetty-hero.webp" alt="Fleetty — terminal fleet monitor" width="100%">

</div>

Fleetty is one Go binary. On the machine in front of you it works like `top`: CPU, memory, disk, network, processes, and a GPU when the hardware can actually report one. Point it at a server and the same screen is served on Fleetty's own SSH port. That port does not open a shell. Monitoring stays read-only until someone authenticates for a management action, and each session keeps its own page, sort, and theme.

## Features

- **Local top.** Per-core load, clocks, memory, disk, network, and processes on Linux and macOS.
- **GPUs.** NVIDIA on Linux, Apple Silicon on macOS. A card appears only after a sample comes back.
- **Hub.** Load, temperature, traffic, and address for many nodes on one screen. Enter opens a node. An offline node keeps its last-seen time and is retried on its own.
- **Slurm.** Several clusters at once, including the jobs on a node and the next eligible one. A narrow terminal still shows the user, the elapsed time, and the job name.
- **Storage map.** On local `fleetty top`, browse what is using disk, find duplicates, archive, and delete. Every write asks for a confirmation.
- **Services.** Mounts, Docker, PM2, and HTTP checks on a NAS profile. `fleettyctl` installs and updates many machines from one manifest, including nodes reached through a login node.
- **Themes.** Dark and light. `t` toggles the current session. The default `auto` follows the terminal, locally and over SSH, and does not need a theme variable forwarded. [Fleetty Dark](themes/ghostty/Fleetty%20Dark) and [Fleetty Light](themes/ghostty/Fleetty%20Light) are optional [Ghostty](https://ghostty.org/) themes for the client. See [Ghostty and themes](docs/en/themes.md).

## Quick start

### Install

Linux and macOS, `amd64` and `arm64`. The script verifies the download against the release `checksums.txt`.

```bash
curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
```

`FLEETTY_VERSION` selects a tag and defaults to `latest`. `FLEETTY_INSTALL_DIR` selects the directory and defaults to `~/.local/bin`. If that directory is not on `PATH`, the script says so.

With a Go toolchain, at the version in [go.mod](go.mod):

```bash
go install github.com/Rhythmicc/fleetty/cmd/fleetty@latest
```

Or take `fleetty_linux_amd64`, `fleetty_linux_arm64`, `fleetty_darwin_amd64`, or `fleetty_darwin_arm64` from [Releases](https://github.com/Rhythmicc/Fleetty/releases) and compare the hash with `checksums.txt` in the same release.

### Run it locally

```bash
fleetty top
```

Local monitoring does not need root and does not listen. `fleetty` with no arguments opens a launcher when the terminal is interactive. The SSH server starts only from `fleetty serve`, or from SSH Server in that launcher.

### Open a remote dashboard

Install on the server, register a client public key, and start the service as described in [Install a Linux node](docs/en/installation.md#linux-ssh-nodes). Then:

```bash
ssh -p 23234 monitor@node.example    # one node
ssh -p 23235 monitor@hub.example     # a Hub
```

The SSH user name labels the session. It is not a login to a system account. The service accepts only keys listed in `authorized_keys`. Read-only monitoring does not ask for the management password.

- [Install a Linux node, as a user or as a system service](docs/en/installation.md#linux-ssh-nodes)
- [Configure a Hub](docs/en/hub.md)
- [Attach a Slurm queue](docs/en/slurm.md)
- [Update nodes from a Hub, including nodes behind a login node](docs/en/deployment.md#updating-nodes-from-a-hub)

## Configuration

`fleetty top` runs without a config file. These flags are optional:

```bash
fleetty top --config machine.json --theme auto --layout layout.json
```

`--theme` is `auto`, `dark`, or `light`, and overrides `DEFAULT_THEME`. `--layout` overrides `FLEETTY_LAYOUT_FILE`. With neither set, the layout file is `fleetty/layout.json` under the user config directory: `~/.config` on Linux (or `$XDG_CONFIG_HOME`), and `~/Library/Application Support` on macOS.

A machine file is JSON. A NAS node looks like this:

```json
{
  "name": "storage-01",
  "profile": "nas",
  "network_interfaces": ["bond0"],
  "mounts": ["/", "/data/shared"],
  "docker": true,
  "pm2_user": "alice",
  "http_checks": [{ "name": "Files", "url": "https://files.example/" }]
}
```

`profile` is `gpu`, `cpu`, `nas`, or `general`. `storage` is `nas`, and `server` is `general`. macOS `fleetty top` with no file uses `general`. A Linux service with no file uses `gpu`. A card is still shown only when collection succeeds.

A user install places the binary at `~/.local/bin/fleetty` and configuration at `~/.config/fleetty`, with a systemd user unit. A system install uses `/opt/fleetty` and `/etc/fleetty`. A node listens on `0.0.0.0:23234`. A Hub listens on `23235`.

History is `history.jsonl` beside the machine file when `MACHINE_CONFIG_FILE` is set, otherwise `~/.config/fleetty/history.jsonl`. `FLEETTY_HISTORY_FILE` selects another path. A Hub reads `HUB_NODES_FILE`.

Every setting, including keys, timeouts, and the management password, is in [Configuration](docs/en/configuration.md).

## Usage

On a node dashboard:

| Key | Action |
| --- | --- |
| `1` / `2` / `3` | Overview / Compute / Network |
| `4` | Storage map. Local `fleetty top` only |
| `Tab` / `Shift+Tab` | Next / previous page. Left and Right do the same, except on the storage map |
| `↑` / `↓`, click | Select a process |
| `Enter` / `Esc` | Process details / back |
| `/` / `c` | Filter / clear the filter |
| `o` / `O` | Next sort field / reverse the order |
| `l` | Layout editor. A NAS profile has no custom layout |
| `m` | Management mode, when `ADMIN_PASSWORD_HASH` is set |
| `t` | Toggle dark and light for this session |
| `r` / `q` | Refresh / quit |

On Compute, 32 logical CPUs or fewer use one horizontal bar per core. More than 32 use a column chart sized to the terminal, with history in a narrow column on the right. `f` focuses a core. `v` switches between the chart and the full per-core list. That choice lasts for the session.

On Linux, the Network page orders TCP connections by live `DOWN + UP` and shows the owning user. A counter the kernel did not provide stays blank. It is not drawn as zero.

The storage map exists only in local `fleetty top`. `d` deletes and `z` writes a `.7z` archive and then deletes the original. Each asks you to type `DELETE` or `ARCHIVE`. `u` scans for duplicate files. On that view, `c` chooses which copy to keep. A remote session cannot delete files. `q` and `Esc` move to the parent directory. At the scan root, they quit.

On a Hub, the arrow keys move between servers, Enter opens the selected server, and `s` opens the Slurm queues when the Hub has any. `t` and `r` are theme and refresh. From a queue, `q` returns to the server list. From the server list, `q` quits.

[The full tour](docs/en/usage.md) · [What management mode is allowed to do](docs/en/security.md)

## Screenshots

These frames are the real TUI, drawn from a fictional demo fleet. Regenerate them with `sh docs/demo/render.sh`.

<a href="docs/images/fleetty-hub.webp">
  <img src="docs/images/fleetty-hub.webp" alt="Acme Research Cluster in Fleetty, with one offline node and a hot GPU" width="100%">
</a>

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>GPU node</strong><br>
      Host, GPUs, the Slurm queue for that node, and processes.<br><br>
      <a href="docs/images/fleetty-gpu.webp">
        <img src="docs/images/fleetty-gpu.webp" alt="Fleetty overview of the fictional node gpu-a01">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>Storage node</strong><br>
      Mounts, Docker, PM2, and HTTP checks, including one unhealthy service.<br><br>
      <a href="docs/images/fleetty-nas.webp">
        <img src="docs/images/fleetty-nas.webp" alt="Fleetty overview of the fictional node storage-01">
      </a>
    </td>
  </tr>
</table>

### Themes

`t` switches the session between dark and light. The Hub below is the same data in each theme.

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>Dark</strong><br><br>
      <a href="docs/images/fleetty-hub.webp">
        <img src="docs/images/fleetty-hub.webp" alt="Fleetty hub in the dark theme">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>Light</strong><br><br>
      <a href="docs/images/fleetty-hub-light.webp">
        <img src="docs/images/fleetty-hub-light.webp" alt="Fleetty hub in the light theme">
      </a>
    </td>
  </tr>
</table>

<img src="docs/images/fleetty-themes.gif" alt="Fleetty hub cycling between the dark and light themes" width="100%">

## Documentation

| Topic | Guide |
| --- | --- |
| Requirements, install, self-update, uninstall | [Install and upgrade](docs/en/installation.md) |
| Pages, per-core CPU, clocks, network, storage map | [Using Fleetty](docs/en/usage.md) |
| Terminal theme, automatic appearance, prompt colors | [Ghostty and themes](docs/en/themes.md) |
| Many servers, and how nodes authenticate the Hub | [Hub](docs/en/hub.md) |
| Several clusters, job state, QOS, narrow queues | [Slurm](docs/en/slurm.md) |
| Docker, PM2, mounts, HTTP checks | [NAS and service checks](docs/en/nas.md) |
| Idempotent install, fleet manifest, staged updates | [Deployment](docs/en/deployment.md) |
| Management password, public keys, privileged helper | [Authentication and management](docs/en/security.md) |
| Settings, service logs, common failures | [Configuration](docs/en/configuration.md) |
| Sample rates, connection reuse, retained history | [Collection and performance](docs/en/performance.md) |

## Contributing

Issues and pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md). When a flag, a config key, or a visible behavior changes, update the English and Chinese docs in the same pull request.

```bash
go test -race ./...
go vet ./...
go build -o fleetty ./cmd/fleetty
go build -o fleettyctl ./cmd/fleettyctl
```

Use the Go version in [go.mod](go.mod).

## License

[MIT](LICENSE)
