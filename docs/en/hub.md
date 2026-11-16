English | [简体中文](../zh-CN/hub.md)

[Back to the README](../../README.md)

# Hub

The Hub is the same binary. It reads each node through that node's existing SSH port, 23234. After you connect, the first screen is a summary of every server. Sections, their order, and which node belongs where come from `groups` and `group` in `nodes.json`. Choose a server to open its full monitor and management screens. The Hub's name is the `name` in that file, so two labs can have two titles.

The rack lists one node per row. The current reading is peak GPU usage, host CPU usage, or combined receive and transmit rate (`NET`) for NAS nodes. The history beside it uses one cell per Hub refresh interval, newest on the right. When space permits, rows also show active users and a one-cell memory pie: `○` empty, `◔` lightly used, `◑` about half, `◕` about three quarters, `●` near full (87.5% or higher), and `—` when unavailable. Node name color follows the highest reported GPU temperature: default below 60°C or when unavailable, green from 60°C, amber from 75°C, and red from 85°C. The separate marker before the name still shows node health. In a wide terminal, the right sidebar displays exact memory usage, GPU temperature, and warning details for the selected node. NAS details still show disk usage and service counts.

The header's `VIEWERS` count includes all open interactive Hub sessions, including yours. Two open terminals count as two sessions; background polling is not counted.

`LIVE` means the last overview poll succeeded, not that the machine can take a new job. `STALE` is counted separately and keeps the last reading. The rack history shares a common time axis: dots are missing samples, not zero usage. Slurm exceptions such as `DRAIN` or `DOWN` appear beside the node name. The metrics sidebar shows the full reported state when available; stale scheduler data is flagged in the queue heading. GPU percentage is peak utilization, not how many GPUs are available for scheduling.

The Hub keeps a Slurm summary below the rack and shows a few jobs when there is room. The `CLUSTER` column is only needed when viewing multiple clusters together. Press `s` to expand the same table in place. It takes more rows while keeping the selected rack node in view when space allows. Press `Enter` on a job for its details and blocker; `Esc` returns to the table, then collapses it without changing the selected node.

The Hub does not need a system SSH account, and it does not store node management passwords on disk. When you enter management mode on a node, the password is sent to that node over the SSH session already in use and checked there. It lives only in the memory of this Hub session.

The Hub connects to nodes with its own RPC key. That key cannot open the interactive monitor, and an operator's key cannot impersonate the Hub RPC identity.

Each node is polled independently. A summary request can take up to three seconds without delaying the other nodes. After a failed request, the rack keeps the last successful reading and marks it `STALE`; three consecutive failures mark the node `OFFLINE`. The Hub backs off probes to an offline node and brings it back when it answers. Press `r` to try again immediately.

Upgrade the monitor nodes to the same version before you deploy the Hub.

## Creating the node configuration

Create the RPC key on the Hub host:

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/fleetty/node_rpc_ed25519
sudo chmod 0600 /etc/fleetty/node_rpc_ed25519
```

Install the contents of `/etc/fleetty/node_rpc_ed25519.pub` into `/etc/fleetty/hub_authorized_keys` on every node, mode `0600`. Add this line to each node's `/etc/fleetty/machine.env`:

```bash
NODE_RPC_AUTHORIZED_KEYS_FILE=/etc/fleetty/hub_authorized_keys
```

Restart the node's `fleetty.service` after the change. The node allows this key set to call a limited RPC as the internal user `fleetty-hub`. It does not offer a shell.

Collect each node's SSH host-key fingerprint on the machine that will run the Hub, and check it through an administrator or another channel you trust before you write it down. `ssh-keyscan` collects. It does not prove identity:

```bash
ssh-keyscan -p 23234 192.0.2.10 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

Copy the example and fill in addresses and fingerprints:

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fLO "${fleetty_release_base}/hub-nodes.example.json"
curl -fLO "${fleetty_release_base}/fleetty-hub.service"

sudo install -o root -g root -m 0600 \
  hub-nodes.example.json \
  /etc/fleetty/nodes.json
sudo editor /etc/fleetty/nodes.json
```

The file looks like this:

```json
{
  "version": 1,
  "name": "Fleetty Hub",
  "refresh_seconds": 1,
  "groups": [
    {
      "id": "gpu-compute",
      "title": "GPU COMPUTE",
      "style": "gpu"
    },
    {
      "id": "cluster-services",
      "title": "CLUSTER SERVICES",
      "style": "network"
    }
  ],
  "nodes": [
    {
      "name": "training-1",
      "group": "gpu-compute",
      "profile": "gpu",
      "description": "Training node",
      "address": "192.0.2.10:23234",
      "slurm_cluster": "Local GPU Cluster",
      "slurm_node": "gpu01",
      "identity_file": "/etc/fleetty/node_rpc_ed25519",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    },
    {
      "name": "storage-1",
      "group": "cluster-services",
      "profile": "nas",
      "description": "Storage and services",
      "address": "192.0.2.20:23234",
      "identity_file": "/etc/fleetty/node_rpc_ed25519",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    }
  ],
  "slurm_clusters": [
    {
      "name": "Local GPU Cluster",
      "description": "Slurm available on the Hub host",
      "transport": "local",
      "refresh_seconds": 2
    },
    {
      "name": "Remote GPU Cluster",
      "description": "Slurm reached through a login node",
      "transport": "ssh",
      "address": "login.example.com:22",
      "user": "slurm-monitor",
      "identity_file": "/etc/fleetty/slurm_remote_ed25519",
      "host_keys": [
        "SHA256:replace-with-the-ed25519-host-key-fingerprint",
        "SHA256:replace-with-the-ecdsa-host-key-fingerprint"
      ],
      "partitions": ["gpu"],
      "refresh_seconds": 2,
      "timeout_seconds": 5
    }
  ]
}
```

`version` is the Hub config format. It must be `1`. `groups` defines the home page completely. Array order is display order. `title` is what people see. `style` is `gpu`, `cpu`, `network`, or `process`, and it only affects how the section looks. Every node must name a group that exists. An unknown group, a duplicate group, an unknown field, or an old config version fails at startup instead of silently choosing another layout.

A node's `profile` is independent of the group. `profile` chooses which detail page the node gets: GPU, NAS, and so on. `group` only chooses where the card sits on the home page. A NAS, a login node, and another service node can share a `CLUSTER SERVICES` section without changing their detail pages. Accepted values are `gpu`, `cpu`, `nas`, and `general`. `storage` means `nas`, and `server` means `general`. An omitted profile is `gpu`.

`refresh_seconds` defaults to 1 and is capped at 60. The remote Slurm example above sets `timeout_seconds` to 5. The Slurm default, when the field is omitted, is 4. See [Slurm](slurm.md).

`identity_file` proves the Hub's identity. The private key must be readable by the Hub's service account and must not be looser than mode `0600`. `host_key` stops the Hub from talking to a machine that is pretending to be the node. When a node generates a new SSH host key, update the fingerprint here.

During a rolling migration on an isolated network, an old node can be reached with the top-level field `"insecure_allow_unauthenticated_nodes": true`. That weakens the Hub-to-node identity check. It is not a permanent setting. Remove it as soon as every node has the RPC public key.

A user-scope Hub reads `~/.config/fleetty/nodes.json`. The paths above are the system unit.

## Installing the Hub service

The Hub listens on 23235 by default, next to a local node monitor on 23234:

```bash
./fleetty_linux_amd64 install \
  --role hub \
  --scope user \
  --config-dir ./hub-config
~/.local/bin/fleetty doctor --role hub --scope user
```

`hub-config` needs at least `authorized_keys` and `nodes.json`, plus any RPC or Slurm private key the file references. System scope adds `sudo` and `--scope system`. The system unit's host key is `/etc/fleetty/hub_host_ed25519`. The user unit uses `~/.config/fleetty/hub_host_ed25519`.

The installer also writes a systemd capability check. Before the Hub starts, it checks that the binary has the current group layout, process table, and pinned footer. `fleettyctl` refuses an old or incomplete build before it replaces the service. A drop-in that stays on disk also refuses an incompatible binary if someone overwrites the main unit by hand. `fleetty version --json` prints the version, the commit, whether the source tree was dirty, and the full capability list.

Connect:

```bash
ssh -p 23235 monitor@hub-host
```

| Key | Action |
| --- | --- |
| `↑` / `↓` | Select a server, or select a job while the queue is expanded |
| `Enter` | Open the selected server or explain the selected job |
| `PgUp` / `PgDn` | Page through servers or through jobs while the queue is expanded |
| `Esc` | Return from a server or job details, collapse the expanded queue, or quit from the home |
| `s` | Expand or collapse the Slurm queue on the home screen |
| `←` / `→` | In the expanded queue, filter by cluster |
| `a` | In the expanded queue, show every cluster |
| `r` | Refresh servers and Slurm queues now |
| `t` | Toggle this session's dark or light theme |
| `q` | Same as `Esc`: leave a job explanation, then the queue, then quit from the home |

Click a row to select it. Click the selected row again or press Enter to open it. When there is room, the list shows the address from the Hub config without the port.

[Configure a Slurm queue](slurm.md) · [Push updates from the Hub](deployment.md#updating-nodes-from-a-hub) · [What the node pages do](usage.md)
