# gpu-ssh-monitor

Read-only GPU/server monitor over SSH. Users connect with a terminal and see a
fixed-size TUI dashboard composed from `nvitop` and `btop`.

![gpu-ssh-monitor preview](assets/monitor.webp)

## Features

- SSH entrypoint powered by Wish
- 130 columns x 69 rows by default
- top 21 rows: `nvitop`
- bottom 48 rows: `btop`
- concurrent SSH sessions share one dashboard source
- stdin is only used for monitor controls: `r` redraws the current client,
  `m` opens the password-protected management menu, and `q` or `Ctrl-C` exits;
  it is not forwarded to `nvitop` or `btop`
- no dashboard collector runs when nobody is connected
- color-preserving differential terminal updates to reduce flicker

## Requirements

- Go 1.25+
- Node.js
- `npm install` build prerequisites for `node-pty`
- `nvitop`
- `btop`

## Build

```bash
npm install
npm run build
```

This produces `./gpu-ssh-monitor`.

## Run

```bash
SSH_PORT=23234 ./gpu-ssh-monitor
```

Connect:

```bash
ssh -p 23234 monitor@host
```

The username is not used for authorization by default. Put the service behind
your normal network controls, firewall, or SSH reverse proxy as appropriate.

## Configuration

- `SSH_HOST` default `0.0.0.0`
- `SSH_PORT` default `23234`
- `SSH_HOST_KEY_PATH` default `.ssh/gpu-ssh-monitor_ed25519`
- `DASHBOARD_CMD` default `NODE_CMD` or `node`
- `DASHBOARD_WORKDIR` default current working directory
- `DASHBOARD_SCRIPT` default `ssh-dashboard.cjs` inside `DASHBOARD_WORKDIR`
- `SSH_DASHBOARD_SOCKET` default `/tmp/gpu-ssh-monitor.sock`
- `SSH_SNAPSHOT_MS` default `SNAPSHOT_MS` or `500`
- `NVITOP_CMD` default `/usr/bin/nvitop`
- `BTOP_CMD` default `/usr/local/bin/btop`
- `NVITOP_ARGS` optional space-separated arguments
- `BTOP_ARGS` optional space-separated arguments
- `COLS` default `130`
- `NVITOP_ROWS` default `21`
- `BTOP_ROWS` default `48`
- `PANE_CWD` default `$HOME`
- `ADMIN_PASSWORD_HASH` bcrypt password hash for management mode (recommended)
- `ADMIN_PASSWORD` plaintext fallback for management mode; avoid this in
  production and prefer `ADMIN_PASSWORD_HASH`
- `ADMIN_RESTART_MONITOR_CMD` default `systemctl restart gpu-ssh-monitor.service`
- `ADMIN_REBOOT_CMD` default `systemctl reboot`
- `ADMIN_POWEROFF_CMD` default `systemctl poweroff`

## Management mode

Set an administrator password to enable it. From the monitor, press `m`, enter
the password, choose an operation, then type `y` to confirm it. The menu is a
Bubble Tea model driven by the existing SSH session, so the live dashboard is
paused while the prompt is visible and restored on exit.

The built-in fixed operations are:

- restart the `gpu-ssh-monitor` systemd service
- reboot the host
- power off the host
- manage processes: list the busiest processes, inspect a PID, or send an
  explicitly confirmed `SIGTERM` to a PID

The commands run as the account that runs this service (the sample unit uses
`root`). They are deliberately not editable by SSH users: change their values
only in the service environment. The password is checked before the menu is
shown and every operation needs a second, explicit `y` confirmation. Each
request is written to the service log with the SSH username and remote address.

Process management is intentionally limited to read-only `ps` queries and a
single `SIGTERM` operation. It only accepts numeric, live PIDs; PID 1 and the
monitor service itself are rejected. Process output is sanitized before it is
rendered, so a process command line cannot inject terminal escape sequences.

For a systemd installation, keep the secret outside the unit file in a
root-readable environment file:

```ini
# /etc/gpu-ssh-monitor/admin.env (chmod 600)
ADMIN_PASSWORD_HASH=$2y$12$replace-this-with-a-bcrypt-hash
# Optional: override these when your unit or service name differs.
# ADMIN_RESTART_MONITOR_CMD=systemctl restart gpu-ssh-monitor.service
# ADMIN_REBOOT_CMD=systemctl reboot
# ADMIN_POWEROFF_CMD=systemctl poweroff
```

For local development only, `ADMIN_PASSWORD=...` is supported as a convenient
fallback. To generate a bcrypt hash when `htpasswd` is available:

```bash
htpasswd -nbBC 12 '' 'choose-a-strong-password' | tr -d ':\n'
```

## systemd

Copy and adjust the sample unit:

```bash
sudo cp deploy/gpu-ssh-monitor.service /etc/systemd/system/gpu-ssh-monitor.service
sudo systemctl daemon-reload
sudo systemctl enable --now gpu-ssh-monitor.service
```

The sample assumes the repository is installed at `/opt/gpu-ssh-monitor` and
the binary is `/opt/gpu-ssh-monitor/gpu-ssh-monitor`.
