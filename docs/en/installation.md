English | [简体中文](../zh-CN/installation.md)

[Back to the README](../../README.md)

# Install and upgrade

## System requirements

- Local monitoring runs on macOS and on common Linux distributions.
- An SSH node, a Hub, and the privileged helper need Linux with systemd.
- `amd64` and `arm64` are the supported architectures.
- NVIDIA metrics need a working driver and `nvidia-smi` on `PATH`.

A normal user can install and run Fleetty. Local macOS monitoring covers CPU, memory, disk, network, processes, and Apple Silicon GPUs. Linux covers NVIDIA GPUs. When a device or a collector is unavailable, the rest of the dashboard still works. Access to the Docker socket, and to another user's process details, is whatever the operating system already allows.

GPU cards follow the samples that actually came back. The default `gpu` profile does not reserve an empty card. On Linux, if `nvidia-smi` fails and the PCI inventory shows no NVIDIA display or 3D controller, Overview, Compute, and a custom page hide the GPU card and the GPU/CUDA marks, and the layout reclaims that space. If an NVIDIA GPU is present, or the PCI inventory cannot be read, the failure stays on screen so a driver, tool, or permission problem is not hidden.

`fleetty top` on macOS, with no machine file, uses the `general` profile. A Linux service with no machine file defaults to `gpu`. Either way, a card is shown only when collection succeeds, subject to the Linux rule above. Accepted profile names are `gpu`, `cpu`, `nas`, and `general`. `storage` is `nas`, and `server` is `general`.

## Download and run locally

The install script checks `checksums.txt`, then copies the binary for this machine to `~/.local/bin/fleetty`. It does not create a service. `FLEETTY_VERSION` defaults to `latest`. `FLEETTY_INSTALL_DIR` defaults to `~/.local/bin`.

```bash
curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
"$HOME/.local/bin/fleetty" top
```

`go install` and a manual download are listed in [Quick start](../../README.md#quick-start).

Add `~/.local/bin` to `PATH` and you can run `fleetty top`. `fleetty` alone opens the launcher, where you can start local monitoring or inspect the SSH server's configuration and keys. The listener starts only after `fleetty serve`, or after you choose SSH Server. Nothing opens an anonymous port in the background.

On a Mac, local monitoring shows the Apple Silicon GPU, battery state, and a CPU frequency weighted by active residency. What appears depends on the hardware and on the system interfaces. A machine with no battery, or no GPU, omits that region. See [CPU and history](usage.md#cpu-and-history) and [themes](themes.md).

`fleetty top` also accepts `--config PATH` for a machine file, `--theme auto|dark|light`, and `--layout PATH`.

## Linux SSH nodes

Install the Linux build from Quick start, then decide which client public keys may connect. The service steps below need systemd. A foreground `fleetty serve` does not.

Copy the client public key to the server. The commands below expect it at `./client.pub`. Then run the installer as the user who will own the service:

```bash
fleetty_config="$(mktemp -d)"
chmod 0700 "${fleetty_config}"
install -m 0600 ./client.pub \
  "${fleetty_config}/authorized_keys"

"$HOME/.local/bin/fleetty" install \
  --role node \
  --scope user \
  --config-dir "${fleetty_config}"
"$HOME/.local/bin/fleetty" doctor --role node --scope user
```

A user install places the binary at `~/.local/bin/fleetty`, configuration at `~/.config/fleetty`, and a `fleetty.service` user unit. The service listens on `0.0.0.0:23234` and accepts only the public keys in `authorized_keys`. Put one key per line to allow more than one person.

If the service should keep running after the user logs out, check lingering:

```bash
loginctl show-user "$USER" -p Linger
```

When that prints `Linger=no`, ask an administrator to enable lingering for the account. You can also run in the foreground. Monitoring itself does not need root:

```bash
SSH_HOST_KEY_PATH="$HOME/.config/fleetty/ssh_host_ed25519" \
SSH_AUTHORIZED_KEYS_FILE="$HOME/.config/fleetty/authorized_keys" \
"$HOME/.local/bin/fleetty" serve
```

Without `SSH_HOST_KEY_PATH`, a foreground `fleetty serve` writes its host key to `.ssh/fleetty_ed25519` in the working directory. The systemd units set the path explicitly, so a service install does not use that fallback.

For a system service, and for actions such as rebooting the host, an administrator can install at system scope:

```bash
sudo "$HOME/.local/bin/fleetty" install \
  --role node \
  --scope system \
  --config-dir "${fleetty_config}"
sudo /opt/fleetty/fleetty doctor --role node --scope system
```

A system install uses `/opt/fleetty` and `/etc/fleetty`. The first start creates an SSH host key in that configuration directory. Do not delete it during an upgrade. The node unit uses `ssh_host_ed25519`. The Hub unit uses `hub_host_ed25519`.

If a firewall is enabled, open TCP 23234 only toward the networks that should reach the monitor.

## Self-update

`fleetty update` replaces the binary you just executed, not a copy in some other directory:

```bash
fleetty update --check             # report only
fleetty update                     # latest stable release
fleetty update --version v0.1.4    # a specific tag
```

The updater downloads the archive for this OS and architecture from the GitHub release, checks SHA-256, and runs the new binary's version check. Only then does it replace the file with an atomic rename on the same filesystem. A failed update leaves the original in place. A second update that overlaps the first is refused. The previous binary is kept at the `Backup` path printed by the command. Symlinks are resolved to the real file. Permissions are preserved, and a root update keeps the original owner.

A user install does not need sudo. A system install is updated by someone who can write the directory:

```bash
sudo /opt/fleetty/fleetty update
```

The update does not change configuration or keys, and it does not restart the service. Sessions that are already open keep the old binary. Restart the unit that matches the install:

```bash
systemctl --user restart fleetty.service       # user node
systemctl --user restart fleetty-hub.service   # user Hub
sudo systemctl restart fleetty.service         # system node
sudo systemctl restart fleetty-hub.service     # system Hub
```

Publishing to many nodes, including nodes behind a login node, is covered in [Deployment](deployment.md).

## Running the installer again

Re-run the installer when a new binary should also refresh the managed unit, or when you are importing configuration. Use a binary you have already downloaded and verified:

```bash
"$HOME/.local/bin/fleetty" install --role node --scope user
```

If nothing managed by the installer changed, the service is not restarted. If the service fails to start, the installer rolls back the binary, the unit, and the configuration it wrote in this run. Omitting a file does not delete an existing public key, RPC key, management file, or SSH host key. A Hub uses `--role hub`. A system service adds `sudo` and `--scope system`.

## Uninstall

```bash
systemctl --user disable --now fleetty.service
rm "$HOME/.config/systemd/user/fleetty.service"
systemctl --user daemon-reload
rm "$HOME/.local/bin/fleetty"
```

To remove the management configuration and the SSH host key as well, delete `~/.config/fleetty`. At system scope, use `sudo systemctl` and the paths `/etc/systemd/system/fleetty.service`, `/opt/fleetty`, and `/etc/fleetty`.

For a Hub, the unit name is `fleetty-hub.service`. User scope still uses `systemctl --user`. System scope uses `sudo systemctl`. `nodes.json` holds node addresses and host-key fingerprints; keep it or delete it as you prefer.
