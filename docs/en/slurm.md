English | [简体中文](../zh-CN/slurm.md)

[Back to the README](../../README.md)

# Slurm queues

A Hub can read several Slurm clusters that do not share a controller. Each cluster is one `slurm_clusters` entry. Node names, partition names, and where the login node sits all come from the config.

## Transports

| Transport | When to use it |
| --- | --- |
| `local` | The Hub is on a Slurm login node, or this machine can run `sinfo` and `squeue` itself |
| `ssh` | Slurm is on another network, or behind a login node. The Hub runs `sinfo` and `squeue` there with a read-only SSH key |

Use a dedicated low-privilege account and a separate key for the remote transport. The private key only has to be readable by the `root` user of the Hub service:

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/fleetty/slurm_remote_ed25519
sudo chmod 0600 /etc/fleetty/slurm_remote_ed25519
```

Add the public key to `authorized_keys` for `slurm-monitor` on the remote login node. Prefix the key with `restrict` so port forwarding, a PTY, and agent forwarding are off, and keep that account's sudo tightly limited. Collect the login node's host-key fingerprints:

```bash
ssh-keyscan -p 22 login.example.com 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

Put every valid `SHA256:` fingerprint from that output into `host_keys`, so a login node that publishes Ed25519, ECDSA, and RSA all match. A single fingerprint can stay in the singular field `host_key`.

Optional `partitions` limits the view to those partitions. Omit it to show every partition and job on that cluster. `refresh_seconds` defaults to 2 and is capped at 60. `timeout_seconds` defaults to 4 and is capped at 30. The [Hub example](hub.md#creating-the-node-configuration) sets a remote timeout of 5, which overrides the default. When one Slurm source is offline, the Hub keeps the last state and retries that source on its own backoff. The server home page and the other clusters are unaffected.

## Associating a node

A compute node joins the queue with `slurm_cluster` and `slurm_node`. `slurm_cluster` must equal some cluster's `name`. `slurm_node` is the node name from `sinfo -N`:

```json
{
  "name": "training-1",
  "group": "gpu-compute",
  "profile": "gpu",
  "address": "192.0.2.10:23234",
  "identity_file": "/etc/fleetty/node_rpc_ed25519",
  "host_key": "SHA256:replace-with-the-node-host-key-fingerprint",
  "slurm_cluster": "Local GPU Cluster",
  "slurm_node": "gpu01"
}
```

With that link, a compute-node panel opened from the Hub shows a short list: jobs running on this node, the earliest eligible candidate whose partition includes this node, and a few jobs still waiting behind it. The standalone Slurm page uses the same colors:

| State | Meaning |
| --- | --- |
| `RUNNING` | Allocated to this node, or running somewhere in the cluster |
| `NEXT` | The earliest eligible candidate in this partition's queue |
| `QUEUED` | Other jobs still waiting |

`NEXT` is computed per cluster and partition. It is the first eligible pending job in the order `squeue` returned for that partition. A cluster can have several `NEXT` jobs at once. A job whose dependency is unmet, or that is held by a job-array concurrency limit, does not take the mark. It is a hint for reading the queue. Slurm still decides, using priority, resources, dependencies, and backfill.

`WEIGHT` is the raw total priority from `squeue %Q`. A higher number usually means a higher scheduling priority. It is not a promise of a start time. Resources, dependencies, and backfill still change the real order.

`QOS` is the quality-of-service name from `squeue %q`, so you can see the priority, time limit, or resource policy chosen at submit time.

## Queue display

When the queue's content width is under 112 columns, the table becomes compact. WEIGHT and the separate QOS column are hidden. A colored dot to the left of the job id stands for QOS, then the columns are JOB ID, USER, S, ELAPSED, and NAME. USER stays on a narrow screen. A long user name is truncated. The state column is one letter: R for RUNNING, N for NEXT, Q for QUEUED, in the same colors as the full names. CLUSTER is added when there is room. The fleet-wide queue and a node's queue follow the same rule. The legend under the table names the QOS values in this queue. `normal` is cyan, `high` is yellow, `urgent` is red, `long` is purple, and `low` or a missing name is gray. Any other site-defined name gets a stable color, and two custom names may share one. Color distinguishes the name. It is not Slurm's priority order. Enter on the fleet-wide queue still opens a detail with the full QOS name and PRIORITY. A wide screen keeps the full columns and the full state names.

The selected row is highlighted across its whole width, including the gaps between columns and the trailing space. The QOS dot and the state text keep their own colors when the row is selected.

`q` or `Esc` on a job explanation returns to the queue. A second press returns to the Hub home. From the home, `q` or `Esc` quits. The rest of the Hub keys are in [Installing the Hub service](hub.md#installing-the-hub-service).

Cluster configuration lives in [the Hub `nodes.json`](hub.md#creating-the-node-configuration).
