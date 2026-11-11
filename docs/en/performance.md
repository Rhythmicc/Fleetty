English | [简体中文](../zh-CN/performance.md)

[Back to the README](../../README.md)

# Collection and performance

- Each node keeps one collector and one cache of the latest snapshot. SSH sessions and Hub RPC share that round, so they do not each run `ps`, `nvidia-smi`, or `nettop`.
- The Hub keeps one persistent SSH connection per node. A dropped connection is redialed. A refresh does not handshake again.
- Host metrics, and Linux connection rates, refresh every second. The process table refreshes every 3 seconds. GPU compute applications, and macOS per-process traffic, refresh every 5 seconds. The screen shows the latest sample it has.
- The node keeps 7 days of minute samples in an append-only JSONL file. The default path is `~/.config/fleetty/history.jsonl`, or beside `machine.json` when `MACHINE_CONFIG_FILE` is set. `FLEETTY_HISTORY_FILE` overrides it. Overview draws the recent CPU history. Hub cards show the node's current load. `fleetty snapshot` includes a `history` field with the last 60 minutes of those samples, not the whole 7-day file.

The ordinary monitor can filter processes and open a read-only detail without a management password and without root. Management mode can send `SIGTERM` to a process the service account is allowed to signal, and it can restart Fleetty. An optional helper, with a narrow allow-list, can also signal another user's process and reboot the host. Every management request is a fixed structured operation. None of them run a shell command you typed. Dangerous operations ask again. PID 1 and the monitor itself cannot be killed from the interface.

Details are in [Authentication and management](security.md).
