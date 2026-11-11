English | [简体中文](../zh-CN/nas.md)

[Back to the README](../../README.md)

# NAS and service checks

A node whose profile is `nas` switches the default pages to a storage-server view: live throughput, cumulative traffic, errors, and drops on the physical interfaces you named; capacity warnings for each mount; and the health of Docker, PM2, and HTTP checks. The Docker table shows the image, health, CPU, memory, network I/O, process count, restarts, uptime, and ports. The PM2 table shows status, instance id, PID, CPU, memory, uptime, restarts, and exec mode. Management mode can still inspect and stop processes the account is allowed to manage, and it can restart the monitor service. Rebooting the host depends on the install scope and on an explicit grant. See [Authentication and management](security.md).

`profile` accepts `gpu`, `cpu`, `nas`, and `general`. `storage` is an alias of `nas`. An omitted profile is `gpu`, so a NAS node has to set `"profile": "nas"`. A NAS profile cannot open the custom layout editor. `l` reports that those layouts are not available yet.

Download the example and edit it for the machine. The commands below are system scope. For a user install, write the files under `~/.config/fleetty` and restart with `systemctl --user restart fleetty.service`:

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fLO "${fleetty_release_base}/machine-nas.example.json"

sudo install -o root -g root -m 0600 \
  machine-nas.example.json \
  /etc/fleetty/machine.json
printf '%s\n' \
  'MACHINE_CONFIG_FILE=/etc/fleetty/machine.json' |
  sudo tee /etc/fleetty/machine.env >/dev/null
sudo chmod 0600 /etc/fleetty/machine.env
sudo systemctl restart fleetty.service
```

The example:

```json
{
  "name": "Storage NAS",
  "profile": "nas",
  "network_interfaces": ["eno1"],
  "mounts": ["/", "/mnt/storage-1", "/mnt/storage-2"],
  "docker": true,
  "pm2_user": "service-account",
  "http_checks": [
    {"name": "Web console", "url": "http://127.0.0.1/"},
    {"name": "Metrics", "url": "http://127.0.0.1:3000/"}
  ]
}
```

Mount paths have to be absolute. An `http_checks` entry needs a name and an `http` or `https` URL with a host. When the service account can read the local Docker socket, Fleetty collects container state and resource counters read-only. You do not have to run Fleetty as root for that. With `pm2_user` set, Fleetty looks for a PM2 daemon that user is already running and reads application state with `pm2 jlist`. A rootless setup should name that same PM2 user. Fleetty does not start the PM2 daemon, and it does not change or restart applications. HTTP checks run on the node, so they can reach a service bound to `127.0.0.1`. Configure only URLs you trust. The monitor does not read the full response body, and it does not display or store environment variables from containers or PM2 applications.

The service account on a system node is allowed `CAP_SETUID` and `CAP_SETGID` so it can drop to `pm2_user` before running that user's PM2 client. That is the only reason those capabilities are on the unit.
