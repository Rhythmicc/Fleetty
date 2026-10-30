# 采集与性能

[返回 README](../README.md)

- 每个节点只维护一份采集器和最近快照缓存：多个 SSH 会话与 Hub RPC 共享同一轮采集，避免每个会话重复执行 `ps`、`nvidia-smi` 和 `nettop`；
- Hub 到每个节点复用一条持久 SSH 连接，连接断开后会自动重连，不再每轮刷新都重新握手；
- 主机级指标和 Linux 连接流量保持每秒刷新；进程表每 3 秒刷新；GPU compute-apps、macOS 进程流量归因每 5 秒刷新，界面显示最近一次采集结果。
- 节点本地保留最近 7 天的分钟级历史（JSONL 追加写入，默认 `~/.config/fleetty/history.jsonl`，可用 `FLEETTY_HISTORY_FILE` 指定）；Overview 显示 CPU 近期历史，Hub 卡片显示节点当前负载摘要，`fleetty snapshot` 的 `history` 字段可直接读取。

普通监控界面可以过滤进程并查看只读详情，不需要管理密码或 root 权限。管理模式可以向当前服务账户有权管理的进程发送 `SIGTERM`，并重启 Fleetty；可选的最小权限助手可以额外授权跨用户终止进程和重启主机。所有管理请求均为固定的结构化操作，不执行用户提供的 shell 命令。危险操作需要再次确认，PID 1 和监控程序自身不能从界面终止。
