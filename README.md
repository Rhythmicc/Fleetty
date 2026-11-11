English | [简体中文](README.zh-CN.md)

<div align="center">

# Fleetty

Local top, and a remote dashboard for hosts, GPUs, storage, and Slurm.

[![CI](https://img.shields.io/github/actions/workflow/status/Rhythmicc/Fleetty/ci.yml?branch=main&label=CI)](https://github.com/Rhythmicc/Fleetty/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Rhythmicc/Fleetty)](https://github.com/Rhythmicc/Fleetty/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Rhythmicc/Fleetty)](go.mod)
[![License](https://img.shields.io/github/license/Rhythmicc/Fleetty)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Rhythmicc/fleetty)](https://goreportcard.com/report/github.com/Rhythmicc/fleetty)

<img src="docs/images/fleetty-hero.webp" alt="Fleetty — terminal fleet monitor" width="100%">

</div>

Fleetty is a terminal console for a workstation, a compute server, a storage node, or a Slurm cluster. One binary runs locally, like `top`, or serves a dashboard on its own SSH port. That port never opens a system shell. Read-only monitoring and management are separate, and each session keeps its own layout and theme.

## Features

- **Local top.** CPU, per-core load, clock, memory, network, and processes on Linux and macOS.
- **GPUs.** NVIDIA on Linux and Apple Silicon on macOS. A card appears only when the machine can actually report it.
- **Hub.** One view of load, temperature, network, and address across many nodes, with a keypress into any node.
- **Slurm.** Several clusters at once, including jobs on a node and the next eligible candidate. Narrow terminals still show the user, elapsed time, and job name.
- **Storage map.** Browse space, find duplicates, archive, and delete on the local machine. Every write asks for an explicit confirmation.
- **Services.** NAS mounts, Docker, PM2, and HTTP checks, plus an installer, a health check, and staged fleet updates.

## Quick start

### Local monitoring

Linux and macOS, `amd64` and `arm64`. This downloads the latest release, checks SHA-256, and installs it for the current user:

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

Local monitoring does not need root and does not listen on the network:

```bash
"$HOME/.local/bin/fleetty" top
```

Add `~/.local/bin` to `PATH` and the command is `fleetty top`. `fleetty` with no arguments opens a launcher. The SSH server starts only when you run `fleetty serve`, or choose SSH Server in that launcher.

### A remote node and a Hub

Install on the server, register a client public key, and start the service as described in [Install a Linux node](docs/en/installation.md#linux-ssh-nodes). Then connect:

```bash
ssh -p 23234 monitor@node-host    # one node
ssh -p 23235 monitor@hub-host     # a Hub of many servers
```

The SSH user name labels the session. It does not log into a system account. The service accepts only registered public keys. Read-only monitoring does not need the management password; writes are authenticated separately.

- [Install a Linux node, as a user or as a system service](docs/en/installation.md#linux-ssh-nodes)
- [Configure a Hub](docs/en/hub.md)
- [Attach a Slurm queue](docs/en/slurm.md)
- [Have the Hub update nodes, including compute nodes behind a login node](docs/en/deployment.md#updating-nodes-from-a-hub)

## Ghostty and themes

[Ghostty](https://ghostty.org/) is the terminal we recommend. The repository includes [Fleetty Dark](themes/ghostty/Fleetty%20Dark) and [Fleetty Light](themes/ghostty/Fleetty%20Light): charcoal and warm paper, with a black cursor in the light theme.

Install the themes on the **client**, then set Ghostty's `theme`:

```ini
theme = dark:Fleetty Dark,light:Fleetty Light
```

Ghostty follows the system appearance, and Fleetty's default `auto` theme follows the terminal, locally and over SSH. No theme variable has to be forwarded. Press `t` and the current session switches by hand instead.

[Download, install, and Powerlevel10k colors](docs/en/themes.md). `background-opacity = 1` keeps contrast stable. Transparency, fonts, and keybindings stay yours to set.

## Screenshots

These are captured from a running Fleetty.

The Hub groups machines by type and keeps the last-seen time when a node drops offline. It keeps retrying on its own.

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

## Keys

| Key | Action |
| --- | --- |
| `1` / `2` / `3` | Overview / Compute / Network |
| `4` | Local Storage Map |
| `Tab` / `Shift+Tab` | Next / previous page |
| `↑` / `↓`, click | Select a process or a node |
| `Enter` / `Esc` | Details / back |
| `/` / `c` | Filter / clear the filter |
| `o` / `O` | Next sort field / reverse the order |
| `l` / `t` / `r` / `q` | Layout / theme / refresh / quit |

On Compute, 32 logical CPUs or fewer use a horizontal bar per core. More than 32 use a compact column chart: the columns size themselves to the terminal, and the history sits in a narrow column on the right. `f` focuses a core. `v` switches to the full per-core list.

Network shows live rates and cumulative traffic. On Linux, TCP connections are ordered by live `DOWN + UP` and include the owning user. Missing counters stay missing. They are not drawn as zero.

The Storage Map exists only in local `fleetty top`. Delete, archive, and dedupe each have their own confirmation. A remote session cannot delete files.

[The full tour](docs/en/usage.md) · [Management and what it is allowed to do](docs/en/security.md)

## Updates

```bash
fleetty update --check             # report only
fleetty update                     # latest stable release
fleetty update --version v0.1.4    # a specific tag
```

An update checks the archive, replaces the binary in place, and leaves the previous binary as a backup. It does not touch configuration or keys, and it does not restart the service. Restart the unit yourself. Clients pick up the new build on the next connection. A system install is updated by someone who can write that directory.

`fleettyctl update` updates many machines. The Hub downloads each archive once and pushes it outward, including through a login node.

[Install and upgrade one machine](docs/en/installation.md) · [Fleet rollout and scheduled updates](docs/en/deployment.md)

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

## Build from source

Use the Go version declared in [go.mod](go.mod).

```bash
go test -race ./...
go vet ./...
go build -o fleetty ./cmd/fleetty
go build -o fleettyctl ./cmd/fleettyctl
```

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Bug reports and pull requests are welcome. Please update the English and Chinese docs together when a flag, a config key, or a visible behavior changes.

## License

[MIT](LICENSE)
