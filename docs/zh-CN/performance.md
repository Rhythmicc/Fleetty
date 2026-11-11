[English](../en/performance.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 采集与性能

- 每个节点只保留一份采集器和一份最新快照缓存。SSH 会话和 Hub RPC 共用这一轮，因此不会各自去跑 `ps`、`nvidia-smi` 或 `nettop`。
- Hub 到每个节点保持一条持久 SSH 连接。连接断开后会重拨。一次刷新不会重新握手。
- 主机指标和 Linux 连接速率每秒刷新。进程表每 3 秒刷新。GPU 计算应用和 macOS 的进程流量每 5 秒刷新。界面显示它手头最新的一次采样。
- 节点以只追加的 JSONL 保留 7 天的分钟级样本。默认路径是 `~/.config/fleetty/history.jsonl`；设置了 `MACHINE_CONFIG_FILE` 时，文件放在 `machine.json` 旁边。`FLEETTY_HISTORY_FILE` 可以覆盖。Overview 画近期 CPU 历史。Hub 卡片显示节点当前负载。`fleetty snapshot` 的 `history` 字段是这些样本的最近 60 分钟，不是整份 7 天文件。

普通监控界面可以过滤进程并打开只读详情，不需要管理密码，也不需要 root。管理模式可以向服务账户有权发信号的进程发送 `SIGTERM`，并可以重启 Fleetty。可选的助手使用很窄的允许列表，还可以向其他用户的进程发信号并重启主机。每个管理请求都是固定的结构化操作。它们都不会执行你输入的 shell 命令。危险操作会再确认一次。PID 1 和监控程序自身不能从界面上终止。

细节见 [认证与管理](security.md)。
