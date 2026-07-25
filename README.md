# GPU SSH Monitor

GPU SSH Monitor 是一个面向 GPU 服务器的 SSH 监控界面。连接专用 SSH 端口后会直接进入终端仪表盘，不会获得服务器 shell。

它以单一可执行文件运行，适合为运维人员或服务器使用者提供统一、受控的主机状态入口。

## 功能概览

- CPU 使用率及 1/5/15 分钟负载；
- 内存与根磁盘容量、使用率；
- 网络实时收发速率、累计流量和最近 60 秒趋势；
- NVIDIA GPU 利用率、显存、核心频率、温度和功率；
- 按 CPU 排序的进程列表及状态颜色；
- 随终端尺寸自动调整的响应式布局；
- 每个 SSH 会话独立的深色、浅色主题；
- 鼠标和键盘操作；
- 密码保护的管理模式。

管理模式可以查看进程详情、过滤进程、发送 `SIGTERM`、重启监控服务或重启主机。所有危险操作都需要再次确认，PID 1 和监控程序自身不能从界面终止。

## 系统要求

- 使用 systemd 的 Linux；
- `amd64` 或 `arm64` 架构；
- root 权限，用于安装和运行服务；
- NVIDIA GPU 指标需要系统已安装驱动并能执行 `nvidia-smi`。

没有 NVIDIA GPU 或 `nvidia-smi` 时，GPU 区域会显示不可用，CPU、内存、磁盘、网络和进程监控仍可正常使用。

## 安装

从 [GitHub Releases](https://github.com/Rhythmicc/gpu-ssh-monitor/releases) 下载当前架构的最新版本：

```bash
case "$(uname -m)" in
  x86_64) monitor_arch=amd64 ;;
  aarch64|arm64) monitor_arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

monitor_release_base="https://github.com/Rhythmicc/gpu-ssh-monitor/releases/latest/download"

curl -fL \
  -o "/tmp/gpu-ssh-monitor_linux_${monitor_arch}" \
  "${monitor_release_base}/gpu-ssh-monitor_linux_${monitor_arch}"
curl -fL \
  -o /tmp/gpu-ssh-monitor.service \
  "${monitor_release_base}/gpu-ssh-monitor.service"
curl -fL \
  -o /tmp/gpu-ssh-monitor-checksums.txt \
  "${monitor_release_base}/checksums.txt"

(
  cd /tmp
  grep "gpu-ssh-monitor_linux_${monitor_arch}$" gpu-ssh-monitor-checksums.txt |
    sha256sum -c -
)
```

安装程序和 systemd 服务：

```bash
sudo install -d -o root -g root -m 0755 /opt/gpu-ssh-monitor
sudo install -d -o root -g root -m 0700 /etc/gpu-ssh-monitor
sudo install -o root -g root -m 0755 \
  "/tmp/gpu-ssh-monitor_linux_${monitor_arch}" \
  /opt/gpu-ssh-monitor/gpu-ssh-monitor
sudo install -o root -g root -m 0644 \
  /tmp/gpu-ssh-monitor.service \
  /etc/systemd/system/gpu-ssh-monitor.service

sudo systemctl daemon-reload
sudo systemctl enable --now gpu-ssh-monitor.service
sudo systemctl status gpu-ssh-monitor.service
```

服务默认监听 `0.0.0.0:23234`。首次启动时会自动创建 `/etc/gpu-ssh-monitor/ssh_host_ed25519`，请勿在升级时删除该文件，否则 SSH host key 会发生变化。

如果服务器启用了防火墙，请只向需要访问监控的网络开放 TCP 23234 端口。

## 连接与操作

监控端口不会登录系统账户，SSH 用户名只用于标记会话来源；连接后不会获得 shell：

```bash
ssh -p 23234 monitor@example-host
```

### 监控页面

| 按键 | 功能 |
| --- | --- |
| `m` | 进入管理模式 |
| `t` | 切换当前会话的深色、浅色主题 |
| `r` | 立即刷新 |
| `q` | 退出 |

主题只影响当前 SSH 会话，不会改变其他已连接用户的界面。

只读监控页面不要求管理密码。请使用防火墙或安全组限制 23234 端口的访问范围；管理密码只保护管理模式中的操作。

### 管理模式

| 按键 | 功能 |
| --- | --- |
| `/` | 输入进程名、用户或 PID 进行过滤 |
| `↑` / `↓` | 选择进程 |
| `Enter` | 查看进程详情 |
| `t` | 在进程详情页请求终止进程 |
| `c` | 清除进程过滤条件 |
| `1` | 重启监控服务 |
| `2` | 重启主机 |
| `Esc` | 返回上一层 |

支持鼠标的终端也可以直接点击进程和按钮。若 SSH 客户端不支持终端鼠标协议，请使用键盘操作。

## 启用管理模式

管理模式默认关闭，配置管理密码后才会显示密码入口。推荐使用 bcrypt 哈希，不要保存明文密码。

Debian 或 Ubuntu 可以使用 `apache2-utils` 生成密码哈希；Fedora、RHEL 等系统可安装提供 `htpasswd` 的 `httpd-tools`：

```bash
sudo apt-get install apache2-utils

read -rsp "Management password: " monitor_admin_password
echo
monitor_admin_hash="$(
  htpasswd -bnBC 12 monitor "${monitor_admin_password}" | cut -d: -f2
)"
unset monitor_admin_password

printf '%s\n' \
  "ADMIN_PASSWORD_HASH=${monitor_admin_hash}" \
  "ADMIN_RESTART_MONITOR_CMD=systemctl restart gpu-ssh-monitor.service" \
  "ADMIN_REBOOT_CMD=systemctl reboot" |
  sudo tee /etc/gpu-ssh-monitor/admin.env >/dev/null
sudo chmod 0600 /etc/gpu-ssh-monitor/admin.env
sudo systemctl restart gpu-ssh-monitor.service
```

监控服务以 root 运行，才能显示全部进程并执行受控管理操作。建议限制监听端口的来源范围，并妥善保管管理密码。

## 配置

默认 systemd 配置位于 `/etc/systemd/system/gpu-ssh-monitor.service`，管理配置位于 `/etc/gpu-ssh-monitor/admin.env`。

| 配置项 | 默认值 | 说明 |
| --- | --- | --- |
| `SSH_HOST` | `0.0.0.0` | 监听地址 |
| `SSH_PORT` | `23234` | 监听端口 |
| `SSH_HOST_KEY_PATH` | `/etc/gpu-ssh-monitor/ssh_host_ed25519` | SSH host key |
| `DEFAULT_THEME` | `dark` | 新连接的默认主题，可设为 `light` |
| `ADMIN_PASSWORD_HASH` | 空 | bcrypt 管理密码哈希；为空时禁用管理模式 |
| `ADMIN_RESTART_MONITOR_CMD` | `systemctl restart gpu-ssh-monitor.service` | 重启监控服务命令 |
| `ADMIN_REBOOT_CMD` | `systemctl reboot` | 重启主机命令 |

修改 systemd 配置后执行：

```bash
sudo systemctl daemon-reload
sudo systemctl restart gpu-ssh-monitor.service
```

## 升级

重新执行安装章节中的下载和校验步骤，然后替换二进制：

```bash
sudo install -o root -g root -m 0755 \
  "/tmp/gpu-ssh-monitor_linux_${monitor_arch}" \
  /opt/gpu-ssh-monitor/gpu-ssh-monitor
sudo systemctl restart gpu-ssh-monitor.service
```

升级不会覆盖 `/etc/gpu-ssh-monitor` 中的管理配置和 SSH host key。

## 故障排查

查看服务状态和最近日志：

```bash
sudo systemctl status gpu-ssh-monitor.service
sudo journalctl -u gpu-ssh-monitor.service -n 100 --no-pager
```

- 无法连接：检查服务状态、TCP 23234 端口和防火墙规则；
- GPU 区域不可用：确认 `nvidia-smi` 能以 root 身份正常执行；
- 管理模式不可用：检查 `/etc/gpu-ssh-monitor/admin.env` 的权限和 `ADMIN_PASSWORD_HASH`；
- 鼠标无法点击：改用键盘，或检查终端及 SSH 客户端的鼠标协议支持；
- 界面字符错位：使用支持 Unicode 和等宽字符的终端字体。

## 卸载

```bash
sudo systemctl disable --now gpu-ssh-monitor.service
sudo rm /etc/systemd/system/gpu-ssh-monitor.service
sudo systemctl daemon-reload
sudo rm -r /opt/gpu-ssh-monitor
```

如需同时删除管理配置和 SSH host key，再删除 `/etc/gpu-ssh-monitor`。
