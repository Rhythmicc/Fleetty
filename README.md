# GPU SSH Monitor

一个纯 Go 实现的 GPU 服务器 SSH 监控界面。每个 SSH 连接直接运行独立的 Bubble Tea TUI，不依赖 Node.js、xterm、nvitop 或 btop，也不会向连接用户开放 shell。

## 功能

- 每秒刷新 CPU、1/5/15 分钟负载、内存和根磁盘使用情况；
- 显示网络实时收发速率、累计流量和最近 60 秒趋势；
- 显示 NVIDIA GPU 利用率、显存、核心频率、温度、实时功率与功率上限；
- 按 CPU 排序显示进程，并以颜色区分运行、睡眠、等待、停止、僵尸和空闲状态；
- 根据终端宽高自动调整列数、GPU 信息和进程行数；
- 每个 SSH 会话可独立切换深色或浅色主题；
- 密码保护的管理模式支持进程过滤、详情查看、`SIGTERM`、重启服务和重启主机；
- 鼠标和键盘均可操作。

普通视图只读取系统状态，不接受任意命令。PID 1 和监控程序自身受到保护，不能从界面终止。

## 使用

```bash
ssh -p 23234 monitor@example-host
```

常用按键：

| 按键 | 功能 |
| --- | --- |
| `m` | 进入管理模式 |
| `t` | 为当前 SSH 会话切换深色/浅色主题 |
| `r` | 立即刷新 |
| `q` | 退出 |
| `/` | 在管理模式中过滤进程 |
| `↑` / `↓` | 选择进程 |
| `Enter` | 查看选中进程详情 |
| `Esc` | 返回上一层 |

鼠标操作依赖 SSH 客户端对终端鼠标协议的支持，键盘操作始终可用。

## 从 GitHub Release 部署

支持 Linux `amd64` 和 `arm64`。服务器需要 systemd；GPU 指标还需要 NVIDIA 驱动提供的 `nvidia-smi`。没有 NVIDIA GPU 时，其余监控功能仍可使用。

以下命令中的版本号需要替换为实际 [Release](https://github.com/Rhythmicc/gpu-ssh-monitor/releases)：

```bash
monitor_version=v0.1.0

case "$(uname -m)" in
  x86_64) monitor_arch=amd64 ;;
  aarch64|arm64) monitor_arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

monitor_release_base="https://github.com/Rhythmicc/gpu-ssh-monitor/releases/download/${monitor_version}"

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

安装二进制和 systemd 服务：

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

首次启动时会在 `/etc/gpu-ssh-monitor/ssh_host_ed25519` 创建 SSH host key。默认监听 `0.0.0.0:23234`，请按实际网络策略开放或限制该端口。

### 启用管理模式

未配置密码时，管理模式保持禁用。推荐用 bcrypt 哈希而不是明文密码。Debian/Ubuntu 可以使用 `apache2-utils` 中的 `htpasswd` 生成：

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

监控服务以 root 运行，才能查看全部进程并执行受控的管理操作。建议限制端口来源、妥善保管管理密码，并保留危险操作的确认步骤。

### 升级

下载并校验新版本后替换二进制，再重启服务：

```bash
sudo install -o root -g root -m 0755 \
  "/tmp/gpu-ssh-monitor_linux_${monitor_arch}" \
  /opt/gpu-ssh-monitor/gpu-ssh-monitor
sudo systemctl restart gpu-ssh-monitor.service
```

配置和 SSH host key 都位于 `/etc/gpu-ssh-monitor`，升级二进制不会覆盖它们。

## 配置

systemd 模板位于 [`deploy/gpu-ssh-monitor.service`](deploy/gpu-ssh-monitor.service)。可通过服务环境变量或 `/etc/gpu-ssh-monitor/admin.env` 调整：

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `SSH_HOST` | `0.0.0.0` | SSH 监听地址 |
| `SSH_PORT` | `23234` | SSH 监听端口 |
| `SSH_HOST_KEY_PATH` | `.ssh/gpu-ssh-monitor_ed25519` | SSH host key 路径；systemd 模板使用 `/etc/gpu-ssh-monitor/ssh_host_ed25519` |
| `DEFAULT_THEME` | `dark` | 新 SSH 会话的默认主题，可设为 `light` |
| `ADMIN_PASSWORD_HASH` | 空 | bcrypt 管理密码哈希 |
| `ADMIN_PASSWORD` | 空 | 兼容用明文密码，不推荐 |
| `ADMIN_RESTART_MONITOR_CMD` | `systemctl restart gpu-ssh-monitor.service` | 重启监控服务命令 |
| `ADMIN_REBOOT_CMD` | `systemctl reboot` | 重启主机命令 |

## 从源码构建

需要 Go 1.25 或更高版本：

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -o gpu-ssh-monitor ./cmd/wish-monitor
```

## 发布 Release

推送 `v*` 标签会触发 [Release workflow](.github/workflows/release.yml)。工作流会运行测试和静态检查，构建 Linux `amd64`、`arm64` 二进制，生成 SHA-256 校验文件，并通过 GitHub CLI 创建带自动发布说明的 Release。

```bash
git tag -a v0.1.0 -m "v0.1.0"
git push origin v0.1.0
```

工作流使用仓库自动提供的 `GITHUB_TOKEN`，不需要额外配置发布密钥。
