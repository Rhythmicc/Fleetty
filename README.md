# GPU SSH Monitor v2

一个直接通过 SSH 使用的 GPU 服务器监控终端界面。v2 是纯 Go 实现：每个 SSH 会话直接运行 Bubble Tea TUI，不使用 Node.js、xterm、nvitop 或 btop，也不会向用户开放 shell。

## 进入监控

```bash
ssh -p 23234 monitor@example-host
```

连接后会立即进入只读仪表盘，展示：

- CPU 使用率和 1/5/15 分钟负载；
- 内存、根磁盘容量与使用率；
- 聚合网络收发速率；
- NVIDIA GPU 利用率、显存、温度与功率；
- 按 CPU 排序的进程摘要。

数据每两秒刷新一次。普通视图不会执行用户输入的命令，也没有进程终止或 shell 功能。

## 管理模式

按 `m` 输入管理密码后，可以点击卡片（终端支持鼠标时）或按数字键选择固定动作：

1. 重启监控服务；
2. 重启机器。

执行前还需要在确认页点击/按 `y` 确认。`Esc` 随时返回只读监控。鼠标点击依赖 SSH 客户端对终端鼠标协议的支持；键盘操作始终可用。

用 bcrypt 哈希配置密码，并按实际服务名覆写动作：

```ini
# /etc/gpu-tui-monitor/admin.env (root:root, 0600)
ADMIN_PASSWORD_HASH=$2a$...
ADMIN_RESTART_MONITOR_CMD=systemctl restart gpu-tui-monitor.service
# 可选；默认是 systemctl reboot
ADMIN_REBOOT_CMD=systemctl reboot
```

未配置 `ADMIN_PASSWORD_HASH`（或兼容的 `ADMIN_PASSWORD`）时，管理模式不会启用。

## 开发与构建

需要 Go 1.25+。GPU 指标要求主机可执行 `nvidia-smi`；没有 NVIDIA 驱动时，界面会显示该区域不可用，其余指标不受影响。

```bash
go test ./...
go build ./cmd/wish-monitor
```

## systemd

参考 [deploy/gpu-ssh-monitor.service](deploy/gpu-ssh-monitor.service)。部署时将 `WorkingDirectory`、`EnvironmentFile` 和 `ExecStart` 调整为实际安装目录。
