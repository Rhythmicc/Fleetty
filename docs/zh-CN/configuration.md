[English](../en/configuration.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 配置与故障排查

## 配置

`fleetty top` 不需要配置文件即可运行，以下标志均为可选：

```bash
fleetty top --config machine.json --theme auto --layout layout.json
```

`--theme` 取 `auto`、`dark` 或 `light`，并覆盖 `DEFAULT_THEME`。`--layout` 覆盖 `FLEETTY_LAYOUT_FILE`；两者均未设置时，布局文件为用户配置目录下的 `fleetty/layout.json`（Linux 为 `~/.config` 或 `$XDG_CONFIG_HOME`，macOS 为 `~/Library/Application Support`）。

对于 `fleetty install`，用户范围的配置在 `~/.config/fleetty`，systemd 单元在 `~/.config/systemd/user`。系统范围使用 `/etc/fleetty` 和 `/etc/systemd/system`：

| 项目 | 用户范围（`--scope user`） | 系统范围（`--scope system`） |
| --- | --- | --- |
| 可执行文件 | `~/.local/bin/fleetty` | `/opt/fleetty/fleetty` |
| 配置目录（权限 `0700`） | `~/.config/fleetty` | `/etc/fleetty` |
| systemd 单元目录 | `~/.config/systemd/user` | `/etc/systemd/system` |
| 节点 / Hub 服务单元 | `fleetty.service` / `fleetty-hub.service` | `fleetty.service` / `fleetty-hub.service` |
| SSH 主机密钥 | `ssh_host_ed25519` / `hub_host_ed25519` | `ssh_host_ed25519` / `hub_host_ed25519` |
| 节点 / Hub 配置文件 | `machine.json` / `nodes.json` | `machine.json` / `nodes.json` |
| 客户端 / Hub RPC 授权公钥 | `authorized_keys` / `hub_authorized_keys` | `authorized_keys` / `hub_authorized_keys` |
| 管理模式凭据 | `admin.env`（`ADMIN_PASSWORD_HASH`） | `admin.env`（`ADMIN_PASSWORD_HASH`） |

下方环境变量表中的路径是系统范围的默认值。用户单元会换成家目录。

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `SSH_HOST` | `0.0.0.0` | 监听地址 |
| `SSH_PORT` | `23234` | 监听端口。Hub 单元使用 `23235` |
| `SSH_HOST_KEY_PATH` | `/etc/fleetty/ssh_host_ed25519` | SSH host key。Hub 单元在同一目录使用 `hub_host_ed25519`。前台 `fleetty serve` 未设置此项时，使用工作目录下的 `.ssh/fleetty_ed25519` |
| `SSH_AUTHORIZED_KEYS_FILE` | systemd 单元中为 `/etc/fleetty/authorized_keys` | 允许打开交互 TUI 的公钥 |
| `NODE_RPC_AUTHORIZED_KEYS_FILE` | 空 | 允许以内部 Hub 身份连接的公钥 |
| `SSH_ALLOW_ANONYMOUS` | `false` | 仅用于隔离网络中的迁移。只有显式设为 `true` 才接受匿名客户端。不能与授权公钥文件或 `SSH_ALLOW_LOCAL_ANONYMOUS` 同时使用 |
| `SSH_ALLOW_LOCAL_ANONYMOUS` | `false` | 回环地址上的客户端可以不带密钥打开 TUI。远程客户端仍需要授权公钥，内部 Hub 身份始终需要密钥。仍然必须设置 `SSH_AUTHORIZED_KEYS_FILE`。不要与全局匿名模式一起启用 |
| `SSH_MAX_CONNECTIONS` | `64` | 同时接受的 SSH 连接数。取值 1 到 10000 |
| `SSH_IDLE_TIMEOUT` | `30m` | SSH 空闲超时 |
| `SSH_MAX_TIMEOUT` | `24h` | 单个 SSH 连接的最长时间 |
| `DEFAULT_THEME` | `auto` | 新连接跟随客户端终端的深浅色。`dark` 或 `light` 固定主题。`fleetty top --theme` 覆盖此项。按过 `t` 之后，会话改为手动选择 |
| `MACHINE_CONFIG_FILE` | 空 | 节点角色、网卡、挂载点和服务检查的 JSON 文件 |
| `HUB_NODES_FILE` | 空 | Hub 节点 JSON。设置后首页变为多服务器视图 |
| `FLEETTY_HISTORY_FILE` | 空 | 节点历史 JSONL。默认在设置了 `machine.json` 时与它同目录，否则为 `~/.config/fleetty/history.jsonl` |
| `FLEETTY_LAYOUT_FILE` | 空 | 本地 `fleetty top` 的仪表盘布局。默认路径在用户配置目录的 `fleetty/layout.json` |
| `ADMIN_PASSWORD_HASH` | 空 | 管理密码的 bcrypt 哈希。为空则关闭管理模式 |
| `ADMIN_PASSWORD` | 空 | 进程环境中的明文管理密码。可以生效，但不建议使用。请用 `ADMIN_PASSWORD_HASH` |
| `FLEETTY_PRIVILEGED_SOCKET` | 空 | 可选的特权助手 Unix socket，通常是 `/run/fleetty/privileged.sock` |

修改用户范围的单元或环境文件之后：

```bash
systemctl --user daemon-reload
systemctl --user restart fleetty.service
```

用户 Hub 的单元是 `fleetty-hub.service`。系统范围使用 `sudo systemctl` 和对应的单元名。

## 故障排查

服务状态和最近日志：

```bash
systemctl --user status fleetty.service
journalctl --user -u fleetty.service -n 100 --no-pager
```

- 无法连接：检查服务、TCP 23234（Hub 为 23235）、防火墙，以及客户端公钥是否在 `authorized_keys` 中。
- GPU 区域不可用：`nvidia-smi` 必须以运行 Fleetty 的用户身份成功。如果 PCI 清单确认没有 NVIDIA GPU，卡片会被隐藏，而不是显示成错误。
- 用户服务在注销后停止：执行 `loginctl show-user "$USER" -p Linger`。
- 管理模式不可用：检查该范围的 `admin.env`、文件权限和 `ADMIN_PASSWORD_HASH`。
- Hub 节点一直离线：Hub 能否到达节点的 TCP 23234，以及 `identity_file`、节点上的 `hub_authorized_keys` 和 `host_key` 指纹是否一致。
- 点击没有反应：改用键盘，或检查终端和 SSH 客户端是否支持鼠标协议。
- 列对不齐：使用支持 Unicode 宽度的等宽字体。

卸载步骤见 [安装与升级](installation.md#卸载)。
