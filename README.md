English | [简体中文](README.zh-CN.md)

<div align="center">

# Fleetty

Terminal monitor for local machines, GPU nodes, storage servers, and Slurm clusters over SSH.

[![CI](https://img.shields.io/github/actions/workflow/status/Rhythmicc/Fleetty/ci.yml?branch=main&label=CI)](https://github.com/Rhythmicc/Fleetty/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Rhythmicc/Fleetty)](https://github.com/Rhythmicc/Fleetty/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Rhythmicc/Fleetty)](go.mod)
[![License](https://img.shields.io/github/license/Rhythmicc/Fleetty)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Rhythmicc/fleetty)](https://goreportcard.com/report/github.com/Rhythmicc/fleetty)

</div>

<a href="docs/images/fleetty-hub.webp">
  <img src="docs/images/fleetty-hub.webp" alt="Fleetty Hub with node health, live cluster links, and Slurm job progress for a fictional research cluster" width="100%">
</a>

When your terminal follows the system's light or dark appearance, Fleetty follows it too. Press <kbd>t</kbd> to switch manually.

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>Dark theme</strong><br><br>
      <a href="docs/images/fleetty-hub-dark.webp">
        <img src="docs/images/fleetty-hub-dark.webp" alt="Fleetty Hub and expanded Slurm queue in the dark theme">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>Light theme</strong><br><br>
      <a href="docs/images/fleetty-hub-light.webp">
        <img src="docs/images/fleetty-hub-light.webp" alt="The same Fleetty Hub and expanded Slurm queue in the light theme">
      </a>
    </td>
  </tr>
</table>

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>GPU node</strong> · GPUs, Slurm queue, processes<br><br>
      <a href="docs/images/fleetty-gpu.webp">
        <img src="docs/images/fleetty-gpu.webp" alt="Fleetty overview of the fictional node gpu-a01">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>Network</strong> · traffic history and connections<br><br>
      <a href="docs/images/fleetty-network.webp">
        <img src="docs/images/fleetty-network.webp" alt="Network traffic history and connection list for a fictional GPU node">
      </a>
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <strong>Storage node</strong> · mounts, Docker, PM2, HTTP<br><br>
      <a href="docs/images/fleetty-nas.webp">
        <img src="docs/images/fleetty-nas.webp" alt="Fleetty overview of the fictional node storage-01">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>Storage map</strong> · directory sizes at a glance<br><br>
      <a href="docs/images/fleetty-storage-map.webp">
        <img src="docs/images/fleetty-storage-map.webp" alt="Fleetty storage treemap and directory size list">
      </a>
    </td>
  </tr>
</table>

Built with Bubble Tea and Lip Gloss for the terminal UI, Wish for SSH, and Charm Log for logging.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
```

See [Install and upgrade](docs/en/installation.md) for `go install`, [Releases](https://github.com/Rhythmicc/Fleetty/releases) binaries, and `FLEETTY_VERSION` / `FLEETTY_INSTALL_DIR`.

## Quick start

```bash
fleetty top                        # local monitor (Linux & macOS, no root)
ssh -p 23234 monitor@node.example  # remote SSH node
ssh -p 23235 monitor@hub.example   # multi-server Hub
```

To deploy a node or Hub on Linux, see [Install a Linux node](docs/en/installation.md#linux-ssh-nodes) and [Configure a Hub](docs/en/hub.md).

## Progress reporting

Processes can report live progress bars to Fleetty with the zero-dependency C/C++ SDK in [sdk/c](sdk/c/README.md). To let a coding agent integrate it for you, copy this prompt:

```text
Integrate Fleetty's C progress SDK (https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/sdk/c/README.md, fleetty_progress.h, fleetty_progress.c) into this project: copy the two source files in, initialize with auto-discovery and 1000 ms throttling, report progress in the main work loop, force-report and close on completion, and keep the workload unaffected if Fleetty is not running.
```

## Keybindings

| Key | Action |
| --- | --- |
| `1` / `2` / `3` / `4` | Overview / Compute / Network / Local Storage Map |
| `Tab` / `Shift+Tab` | Next / previous page |
| `↑` / `↓`, `Enter`, `Esc` | Select / open details or node / back |
| `/` / `c` | Filter processes / clear filter |
| `o` / `O` | Next sort column / reverse sort order |
| `l` / `m` / `t` | Layout editor / management mode / toggle dark & light theme |
| `s` / `r` / `q` | Slurm queue (on Hub) / refresh / quit |

## Documentation

- [Install and upgrade](docs/en/installation.md) — system requirements, `install.sh`, `go install`, Linux SSH nodes, `fleetty update`, uninstall
- [Using Fleetty](docs/en/usage.md) — pages, per-core CPU (`f` / `v`), network sockets, local storage map (`d` / `z` / `u`), custom layouts
- [C progress SDK](sdk/c/README.md) — zero-dependency non-blocking C/C++ process progress reporting (`fleetty_progress.h` / `fleetty_progress.c`)
- [Ghostty and themes](docs/en/themes.md) — automatic dark/light following, Ghostty palettes, Powerlevel10k colors
- [Hub](docs/en/hub.md) — `nodes.json` groups, persistent SSH RPC keys, multi-server navigation
- [Slurm](docs/en/slurm.md) — local and SSH login-node transports, `NEXT` candidates, QOS, compact queues
- [NAS and service checks](docs/en/nas.md) — `machine.json`, physical NICs, mounts, Docker, PM2, HTTP probes
- [Deployment](docs/en/deployment.md) — `fleettyctl`, fleet manifests, cascaded Hub rollouts, Prometheus & JSON export
- [Authentication and management](docs/en/security.md) — `ADMIN_PASSWORD_HASH`, read-only redaction, privileged helper socket
- [Configuration](docs/en/configuration.md) — user vs. system paths, environment variables, systemd logs, troubleshooting
- [Collection and performance](docs/en/performance.md) — sample intervals, connection reuse, 7-day `history.jsonl` retention

## Contributing

Issues and pull requests are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE)
