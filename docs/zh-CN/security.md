[English](../en/security.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 认证、管理模式与特权助手

监控端口只提供 Fleetty 界面。没有系统 shell。交互式客户端公钥和 Hub RPC 公钥分开配置，管理模式另外校验自己的密码。能看这台机器，不等于能管理它。

## 启用管理模式

配置管理密码之前，管理模式是关闭的。密码提示只在配置之后出现。保存 bcrypt 哈希。不要保存明文密码。

进程环境里的 `ADMIN_PASSWORD` 也会被接受。不要使用它。应保留的设置是 `ADMIN_PASSWORD_HASH`。

在 Debian 或 Ubuntu 上，`apache2-utils` 提供 `htpasswd`。在 Fedora、RHEL 及相近系统上，软件包是 `httpd-tools`：

```bash
sudo apt-get install apache2-utils

# Type the password at the prompt. Do not put it in the command line.
monitor_admin_hash="$(
  htpasswd -nBC 12 monitor | cut -d: -f2
)"

printf '%s\n' \
  "ADMIN_PASSWORD_HASH=${monitor_admin_hash}" \
  >"$HOME/.config/fleetty/admin.env"
chmod 0600 "$HOME/.config/fleetty/admin.env"
systemctl --user restart fleetty.service
```

用户范围内，管理模式可以停止该用户拥有的进程，并重启 Fleetty 用户服务。其他用户的进程没有终止入口，也没有重启主机的按钮。系统范围写入 `/etc/fleetty/admin.env`。默认操作是重启系统服务和重启主机。

如果希望 Fleetty 主服务保持非特权，同时允许少量系统操作，可以安装可选的助手。创建一个能够访问助手 socket 的系统组，并把 Fleetty 服务用户加入该组：

```bash
getent group fleetty >/dev/null || sudo groupadd --system fleetty
sudo usermod -aG fleetty "$USER"
```

重新登录，使组成员身份生效，然后用同一个 Fleetty 二进制安装助手：

```bash
sudo "$HOME/.local/bin/fleetty" install \
  --role privileged-helper \
  --scope system
sudo /opt/fleetty/fleetty doctor \
  --role privileged-helper \
  --scope system
```

让用户范围的 `admin.env` 指向本地 socket：

```bash
printf '%s\n' \
  'FLEETTY_PRIVILEGED_SOCKET=/run/fleetty/privileged.sock' \
  >>"$HOME/.config/fleetty/admin.env"
chmod 0600 "$HOME/.config/fleetty/admin.env"
systemctl --user restart fleetty.service
```

## 特权助手

助手是独立的加固服务，单元名为 `fleetty-privileged.service`。它只接受：

- 重启固定单元 `fleetty.service`
- 重启主机
- 向启动时间匹配、且不是 PID 1 的进程发送 `SIGTERM`

socket 的组决定哪些本地账户可以调用助手。默认 socket 是 `/run/fleetty/privileged.sock`，权限 `0660`，组为 `fleetty`。协议不接受可执行文件路径、命令行或 shell 字符串。每次请求都会记录调用者的 UID、GID、PID、操作、目标和结果。

只读进程详情会隐藏工作目录，并遮盖密码、令牌、API key 和类似参数。只有管理密码校验通过后才显示完整详情。

管理密码连续失败 5 次后，会锁定 30 秒。

## 管理按键

| 按键 | 功能 |
| --- | --- |
| `/` | 按进程名、用户或 PID 过滤 |
| `↑` / `↓` | 选择进程 |
| `Enter` | 进程详情 |
| `t` | 在进程详情里请求终止进程 |
| `c` | 清除进程过滤 |
| `1` | 重启监控服务 |
| `2` | 重启主机。仅当该服务账户配置了这个操作时显示 |
| `Esc` | 返回 |

支持鼠标协议的终端可以直接点击进程和按钮。SSH 客户端不支持时，使用键盘。

终止进程会先确认，然后发送 `SIGTERM`。用 `y` 或回车确认。用 `Esc`、`q` 或 `n` 取消。重启服务和重启主机使用同样的确认和取消键。
