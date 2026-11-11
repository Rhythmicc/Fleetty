English | [简体中文](../zh-CN/security.md)

[Back to the README](../../README.md)

# Authentication and management

The monitor port serves the Fleetty interface and nothing else. There is no system shell. Interactive client keys and Hub RPC keys are configured separately, and management mode checks a password of its own. Permission to watch the machine is not permission to administer it.

## Management mode

Management mode is off until a management password is configured. The password prompt appears only then. Store a bcrypt hash. Do not store the password in clear text.

`ADMIN_PASSWORD` in the process environment is also accepted. Do not use it. `ADMIN_PASSWORD_HASH` is the setting to keep.

On Debian or Ubuntu, `apache2-utils` provides `htpasswd`. On Fedora, RHEL, and related systems, the package is `httpd-tools`:

```bash
sudo apt-get install apache2-utils

# Type the password at the prompt. Do not put it in the command line.
monitor_admin_hash="$(
  htpasswd -nBC 12 monitor | cut -d: -f2
)"

printf '%s\n' \
  "ADMIN_PASSWORD_HASH=${monitor_admin_hash}" \
  >"$HOME/.config/fleetty/admin.env"
chmod 0600 "$HOME/.config/fleetty/admin.env"
systemctl --user restart fleetty.service
```

At user scope, management mode can stop processes that user owns and restart the Fleetty user service. Other users' processes have no terminate control, and there is no reboot button. At system scope, write `/etc/fleetty/admin.env`. The default actions are restarting the system service and rebooting the host.

To keep the main Fleetty service unprivileged and still allow a few system operations, install the optional helper. Create a system group that can reach the helper socket, and add the Fleetty service user to it:

```bash
getent group fleetty >/dev/null || sudo groupadd --system fleetty
sudo usermod -aG fleetty "$USER"
```

Log in again so the group membership is visible, then install the helper from the same Fleetty binary:

```bash
sudo "$HOME/.local/bin/fleetty" install \
  --role privileged-helper \
  --scope system
sudo /opt/fleetty/fleetty doctor \
  --role privileged-helper \
  --scope system
```

Point the user-scope `admin.env` at the local socket:

```bash
printf '%s\n' \
  'FLEETTY_PRIVILEGED_SOCKET=/run/fleetty/privileged.sock' \
  >>"$HOME/.config/fleetty/admin.env"
chmod 0600 "$HOME/.config/fleetty/admin.env"
systemctl --user restart fleetty.service
```

## Privileged helper

The helper is its own hardened service, `fleetty-privileged.service`. It accepts only:

- restart the fixed unit `fleetty.service`
- reboot the host
- send `SIGTERM` to a process whose start time matches, and which is not PID 1

The socket's group decides which local accounts can call the helper. The default socket is `/run/fleetty/privileged.sock`, mode `0660`, group `fleetty`. The protocol does not accept an executable path, a command line, or a shell string. Every request is logged with the caller's UID, GID, PID, the operation, the target, and the result.

A read-only process detail hides the working directory and redacts passwords, tokens, API keys, and similar arguments. The full detail is shown only after the management password checks out.

Five failed management passwords lock attempts for 30 seconds.

## Management keys

| Key | Action |
| --- | --- |
| `/` | Filter by process name, user, or PID |
| `↑` / `↓` | Select a process |
| `Enter` | Process details |
| `t` | On the process detail, ask to terminate the process |
| `c` | Clear the process filter |
| `1` | Restart the monitor service |
| `2` | Reboot the host. Shown only when this service account has that action configured |
| `Esc` | Back |

A terminal that supports the mouse protocol can click processes and buttons. If the SSH client does not, use the keyboard.

Terminating a process asks for confirmation and then sends `SIGTERM`. Confirm with `y` or Enter. Cancel with `Esc`, `q`, or `n`. The same confirm and cancel keys apply to restarting the service and rebooting the host.
