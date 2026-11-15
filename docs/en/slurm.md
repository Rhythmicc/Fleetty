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

With that link, the compute-node panel lists jobs running on the node and pending jobs whose requests match the node. The queue uses these labels:

| State | Meaning |
| --- | --- |
| `RUNNING` | Allocated to this node, or running somewhere in the cluster |
| `NEXT` | Still pending, but Slurm reports an estimated start |
| `PENDING` | Waiting without an estimated start; the blocker explains why when known |

`NEXT` is a display label, not a Slurm state or a promise of scheduling order. It uses the estimated start from `squeue %S`, shown in the job details. The time can change as the scheduler reevaluates the queue.

`WEIGHT` is the raw total priority from `squeue %Q`. A higher number usually means a higher scheduling priority. It is not a promise of a start time. Resources, dependencies, and backfill still change the real order.

`QOS` is the quality-of-service name from `squeue %q`, so you can see the priority, time limit, or resource policy chosen at submit time.

## Queue display

In the Hub queue from 112 content columns, `TIME` shows elapsed time / wall-time limit reported by Slurm (for example, `4:20 / 30:00`). On narrower screens, the table only shows elapsed time. Expand the queue with `s` and press Enter on a job to see both values in full.

Under 112 content columns, WEIGHT and the separate QOS column are hidden. A colored dot indicates QOS. The table keeps JOB ID, USER, abbreviated STATE (R for RUNNING, N for NEXT, P for PENDING), ELAPSED, and NAME. The Hub's final NODE column shows allocated nodes from 72 columns, or `—` before allocation. The legend names the QOS values; their colors are not the scheduling order. CLUSTER appears when multiple clusters are shown and there is room. Array suffixes are hidden unless multiple distinct rows in the cluster would otherwise share an ID. Enter in the full queue opens the exact ID, priority, and blocker reason.

A running job with a fresh Fleetty socket report shows a progress bar beside its name (percentage only when space is tight). The reporting Linux node must run the updated Fleetty service, be mapped to the Slurm cluster and node in the Hub, and use numeric Slurm job cgroups. Fleetty verifies the allocation ID, owner UID, and allocated node. If several processes report for one job, the row shows a count rather than inventing a job-wide percentage. Job details show individual counters.

The selected row is highlighted across its whole width, including the gaps between columns and the trailing space. The QOS dot and the state text keep their own colors when the row is selected.

`q` or `Esc` on a job explanation returns to the queue. A second press returns to the Hub home. From the home, `q` or `Esc` quits. The rest of the Hub keys are in [Installing the Hub service](hub.md#installing-the-hub-service).

Cluster configuration lives in [the Hub `nodes.json`](hub.md#creating-the-node-configuration).
