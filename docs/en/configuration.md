English | [简体中文](../zh-CN/configuration.md)

[Back to the README](../../README.md)

# Configuration and troubleshooting

## Configuration

User scope keeps configuration in `~/.config/fleetty` and the systemd unit in `~/.config/systemd/user`. System scope uses `/etc/fleetty` and `/etc/systemd/system`. The paths in the table are the system-scope defaults. A user unit substitutes the home directory.

| Setting | Default | Meaning |
| --- | --- | --- |
| `SSH_HOST` | `0.0.0.0` | Listen address |
| `SSH_PORT` | `23234` | Listen port. The Hub unit uses `23235` |
| `SSH_HOST_KEY_PATH` | `/etc/fleetty/ssh_host_ed25519` | SSH host key. The Hub unit uses `hub_host_ed25519` in the same directory. A foreground `fleetty serve` with this unset uses `.ssh/fleetty_ed25519` in the working directory |
| `SSH_AUTHORIZED_KEYS_FILE` | `/etc/fleetty/authorized_keys` in the systemd unit | Public keys allowed to open the interactive TUI |
| `NODE_RPC_AUTHORIZED_KEYS_FILE` | empty | Public keys allowed to connect as the internal Hub identity |
| `SSH_ALLOW_ANONYMOUS` | `false` | Isolated migration only. Anonymous clients are accepted only when this is explicitly `true`. It cannot be combined with an authorized-keys file or with `SSH_ALLOW_LOCAL_ANONYMOUS` |
| `SSH_ALLOW_LOCAL_ANONYMOUS` | `false` | Loopback clients may open the TUI without a key. Remote clients still need an authorized key, and the internal Hub identity always needs a key. `SSH_AUTHORIZED_KEYS_FILE` is still required. Do not enable this together with global anonymous mode |
| `SSH_MAX_CONNECTIONS` | `64` | Simultaneous SSH connections accepted. Values from 1 to 10000 |
| `SSH_IDLE_TIMEOUT` | `30m` | Idle SSH connection timeout |
| `SSH_MAX_TIMEOUT` | `24h` | Longest a single SSH connection may last |
| `DEFAULT_THEME` | `auto` | New connections follow the client terminal's light or dark appearance. `dark` or `light` pins it. `fleetty top --theme` overrides this. After `t`, the session is chosen by hand |
| `MACHINE_CONFIG_FILE` | empty | JSON file for the node role, interfaces, mounts, and service checks |
| `HUB_NODES_FILE` | empty | Hub node JSON. When set, the home page becomes the multi-server view |
| `FLEETTY_HISTORY_FILE` | empty | Node history JSONL. By default, beside `machine.json` when that file is set, otherwise `~/.config/fleetty/history.jsonl` |
| `FLEETTY_LAYOUT_FILE` | empty | Dashboard layout for local `fleetty top`. The default path is under the user configuration directory, `fleetty/layout.json` |
| `ADMIN_PASSWORD_HASH` | empty | bcrypt hash of the management password. Empty disables management mode |
| `ADMIN_PASSWORD` | empty | Plaintext management password in the process environment. Accepted, and not recommended. Use `ADMIN_PASSWORD_HASH` |
| `FLEETTY_PRIVILEGED_SOCKET` | empty | Optional Unix socket for the privileged helper, usually `/run/fleetty/privileged.sock` |

After editing a user-scope unit or environment file:

```bash
systemctl --user daemon-reload
systemctl --user restart fleetty.service
```

A user Hub is `fleetty-hub.service`. System scope uses `sudo systemctl` and the matching unit name.

## Troubleshooting

Service state and the recent log:

```bash
systemctl --user status fleetty.service
journalctl --user -u fleetty.service -n 100 --no-pager
```

- Cannot connect: the service, TCP port 23234 (23235 for a Hub), the firewall, and whether the client key is in `authorized_keys`.
- GPU region unavailable: `nvidia-smi` has to succeed as the user Fleetty runs as. If the PCI inventory confirms there is no NVIDIA GPU, the card is hidden rather than shown as an error.
- A user service stops at logout: `loginctl show-user "$USER" -p Linger`.
- Management mode unavailable: `admin.env` for that scope, its mode, and `ADMIN_PASSWORD_HASH`.
- A Hub node stays offline: the Hub can reach the node's TCP 23234, and `identity_file`, the node's `hub_authorized_keys`, and the `host_key` fingerprint match.
- Clicks do nothing: use the keyboard, or check that the terminal and the SSH client speak the mouse protocol.
- Columns drift: use a monospace font with Unicode width support.

Uninstall steps are in [Install and upgrade](installation.md#uninstall).
