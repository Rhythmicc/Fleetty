# 认证、管理模式与特权助手

[返回 README](../README.md)

监控端口只提供 Fleetty 界面，不提供系统 shell。交互式客户端公钥和 Hub RPC 公钥分别配置，管理模式另行验证密码；只读监控权限不等于系统管理权限。

## 启用管理模式

管理模式默认关闭，配置管理密码后才会显示密码入口。推荐使用 bcrypt 哈希，不要保存明文密码。

Debian 或 Ubuntu 可以使用 `apache2-utils` 生成密码哈希；Fedora、RHEL 等系统可安装提供 `htpasswd` 的 `httpd-tools`：

```bash
sudo apt-get install apache2-utils

# 交互输入密码，不把明文密码放进命令行参数。
monitor_admin_hash="$(
  htpasswd -nBC 12 monitor | cut -d: -f2
)"

printf '%s\n' \
  "ADMIN_PASSWORD_HASH=${monitor_admin_hash}" \
  >"$HOME/.config/fleetty/admin.env"
chmod 0600 "$HOME/.config/fleetty/admin.env"
systemctl --user restart fleetty.service
```

user scope 的管理模式可以终止该用户拥有的进程，并重启 Fleetty user service；其他用户的进程不会显示终止入口，重启主机按钮默认不存在。system scope 将配置写入 `/etc/fleetty/admin.env`，默认提供重启系统服务和主机的固定操作。

需要在保持 Fleetty 主服务为普通用户的同时授权少量系统操作时，可以安装可选的特权助手。先创建只能访问助手 socket 的系统组，并把 Fleetty 服务用户加入该组：

```bash
getent group fleetty >/dev/null || sudo groupadd --system fleetty
sudo usermod -aG fleetty "$USER"
```

重新登录以刷新用户组，然后使用同一个 Fleetty 二进制安装助手：

```bash
sudo "$HOME/.local/bin/fleetty" install \
  --role privileged-helper \
  --scope system
sudo /opt/fleetty/fleetty doctor \
  --role privileged-helper \
  --scope system
```

在 user scope 的 `admin.env` 中启用本地 socket：

```bash
printf '%s\n' \
  'FLEETTY_PRIVILEGED_SOCKET=/run/fleetty/privileged.sock' \
  >>"$HOME/.config/fleetty/admin.env"
chmod 0600 "$HOME/.config/fleetty/admin.env"
systemctl --user restart fleetty.service
```

助手以独立的加固服务运行，只接受以下操作：

- 重启固定的 `fleetty.service`；
- 重启主机；
- 对经过 PID 启动时间复核且不是 PID 1 的进程发送 `SIGTERM`。

Unix socket 的组权限决定哪些本地账户可以调用助手。协议不接受可执行文件、命令行或 shell 字符串，每次请求都会记录调用进程的 UID、GID、PID、操作类型、目标和结果。

只读进程详情隐藏工作目录，并遮盖密码、令牌、API Key 等常见敏感参数；通过管理密码认证后才显示完整详情。

## 管理按键

| 按键 | 功能 |
| --- | --- |
| `/` | 输入进程名、用户或 PID 进行过滤 |
| `↑` / `↓` | 选择进程 |
| `Enter` | 查看进程详情 |
| `t` | 在进程详情页请求终止进程 |
| `c` | 清除进程过滤条件 |
| `1` | 重启监控服务 |
| `2` | 重启主机；仅在当前服务账户具备并配置该能力时出现 |
| `Esc` | 返回上一层 |

支持鼠标的终端也可以直接点击进程和按钮。若 SSH 客户端不支持终端鼠标协议，请使用键盘操作。
