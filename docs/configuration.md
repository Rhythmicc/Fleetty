# 配置与故障排查

[返回 README](../README.md)

## 配置

user scope 的配置位于 `~/.config/fleetty`，systemd unit 位于 `~/.config/systemd/user`；system scope 分别使用 `/etc/fleetty` 和 `/etc/systemd/system`。下表中的路径展示 system scope 默认值，user unit 会自动换成用户目录。

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `SSH_HOST` | `0.0.0.0` | 监听地址 |
| `SSH_PORT` | `23234` | 监听端口 |
| `SSH_HOST_KEY_PATH` | `/etc/fleetty/ssh_host_ed25519` | SSH host key |
| `SSH_AUTHORIZED_KEYS_FILE` | systemd 服务中为 `/etc/fleetty/authorized_keys` | 允许连接交互 TUI 的客户端公钥 |
| `NODE_RPC_AUTHORIZED_KEYS_FILE` | 空 | 允许以内部 Hub 身份连接节点的公钥 |
| `SSH_ALLOW_ANONYMOUS` | `false` | 仅用于隔离网络迁移；显式设为 `true` 才允许匿名连接 |
| `SSH_ALLOW_LOCAL_ANONYMOUS` | `false` | 允许通过本机回环地址免密查看 TUI；远程连接仍须使用授权公钥，内部 Hub 身份始终须验公钥。仍需设置 `SSH_AUTHORIZED_KEYS_FILE`，不能与全局匿名模式同时启用 |
| `SSH_MAX_CONNECTIONS` | `64` | 同时接受的 SSH 连接上限 |
| `SSH_IDLE_TIMEOUT` | `30m` | SSH 空闲连接超时 |
| `SSH_MAX_TIMEOUT` | `24h` | 单个 SSH 连接的最长持续时间 |
| `DEFAULT_THEME` | `auto` | 新连接跟随客户端终端的深浅色；设为 `dark` / `light` 可固定主题。`fleetty top --theme` 优先于此设置，按 `t` 后当前会话改为手动选择 |
| `MACHINE_CONFIG_FILE` | 空 | 节点角色、网卡、挂载点及服务检查 JSON 配置 |
| `HUB_NODES_FILE` | 空 | Hub 节点 JSON 配置；设置后首页切换为多服务器模式 |
| `FLEETTY_HISTORY_FILE` | 空 | 节点历史 JSONL 路径；默认位于 machine.json 同目录，否则 `~/.config/fleetty/history.jsonl` |
| `ADMIN_PASSWORD_HASH` | 空 | bcrypt 管理密码哈希；为空时禁用管理模式 |
| `FLEETTY_PRIVILEGED_SOCKET` | 空 | 可选的特权助手 Unix socket；通常为 `/run/fleetty/privileged.sock` |

user scope 修改配置后执行：

```bash
systemctl --user daemon-reload
systemctl --user restart fleetty.service
```

## 故障排查

查看服务状态和最近日志：

```bash
systemctl --user status fleetty.service
journalctl --user -u fleetty.service -n 100 --no-pager
```

- 无法连接：检查服务状态、TCP 23234 端口、防火墙规则和客户端公钥是否存在于 `authorized_keys`；
- GPU 区域不可用：确认 `nvidia-smi` 能以运行 Fleetty 的用户身份正常执行；
- user service 在退出后停止：检查 `loginctl show-user "$USER" -p Linger`；
- 管理模式不可用：检查对应 scope 的 `admin.env` 权限和 `ADMIN_PASSWORD_HASH`；
- Hub 节点离线：确认 Hub 能访问节点的 TCP 23234 端口，并检查 `identity_file`、节点的 `hub_authorized_keys` 和 `host_key` 指纹；
- 鼠标无法点击：改用键盘，或检查终端及 SSH 客户端的鼠标协议支持；
- 界面字符错位：使用支持 Unicode 和等宽字符的终端字体。

卸载步骤见[安装与升级](installation.md#卸载)。
