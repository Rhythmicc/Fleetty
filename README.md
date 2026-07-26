# GPU SSH Monitor

GPU SSH Monitor 是一个面向计算服务器和存储节点的 SSH 监控界面。连接专用 SSH 端口后会直接进入终端仪表盘，不会获得服务器 shell。

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
- 密码保护的管理模式；
- 可选的多服务器 Hub 首页；
- 面向 NAS 的网络、存储、Docker 和 HTTP 服务监控页面。

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

## NAS 监控页面

将节点配置为 `nas` 后，默认页面会改为存储服务器视图，重点展示指定物理网卡的实时吞吐、累计流量、错误与丢包，各挂载点的容量告警，以及 Docker 容器和 HTTP 服务的健康状态。管理模式仍然可以查看和终止进程、重启监控服务或重启主机。

下载 NAS 配置示例并按实际环境修改：

```bash
monitor_release_base="https://github.com/Rhythmicc/gpu-ssh-monitor/releases/latest/download"
curl -fLO "${monitor_release_base}/machine-nas.example.json"

sudo install -o root -g root -m 0600 \
  machine-nas.example.json \
  /etc/gpu-ssh-monitor/machine.json
printf '%s\n' \
  'MACHINE_CONFIG_FILE=/etc/gpu-ssh-monitor/machine.json' |
  sudo tee /etc/gpu-ssh-monitor/machine.env >/dev/null
sudo chmod 0600 /etc/gpu-ssh-monitor/machine.env
sudo systemctl restart gpu-ssh-monitor.service
```

配置示例：

```json
{
  "name": "Storage NAS",
  "profile": "nas",
  "network_interfaces": ["eno1"],
  "mounts": ["/", "/mnt/storage-1", "/mnt/storage-2"],
  "docker": true,
  "http_checks": [
    {"name": "Web console", "url": "http://127.0.0.1/"},
    {"name": "Metrics", "url": "http://127.0.0.1:3000/"}
  ]
}
```

服务以 root 运行时可以只读访问 Docker 容器列表。HTTP 检查由节点本机发起，因此可以检查只监听 `127.0.0.1` 的服务。请只配置可信 URL；监控程序不会读取完整响应正文，也不会读取容器环境变量。

## 多服务器 Hub

Hub 使用同一个 Go 可执行文件运行，并通过各节点现有的 23234 SSH 端口读取状态。连接 Hub 后会先看到所有服务器的简报，节点会根据 `profile` 自动分到 GPU 计算、NAS 与存储等区域；选择服务器即可进入该节点的完整监控和管理界面。Hub 名称由配置文件中的 `name` 决定，可以为不同实验室或集群分别命名。

Hub 不需要系统 SSH 账户，也不会在磁盘中保存节点管理密码。进入某个节点的管理模式时，密码会通过加密的 SSH 连接发送给该节点即时校验，并且只保留在当前 Hub 会话的内存中。

关机或暂时不可达的节点会显示为 `OFFLINE`，不会阻塞其他节点的每秒刷新。Hub 会逐步降低离线节点的探测频率，并在节点恢复后自动重新上线；也可以选中节点后按 `r` 立即重试。

部署 Hub 前，应先将各监控节点升级到相同版本。

### 创建节点配置

先在准备运行 Hub 的服务器上取得各节点的 SSH host key 指纹：

```bash
ssh-keyscan -p 23234 192.0.2.10 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

复制示例配置并填写节点地址与指纹：

```bash
monitor_release_base="https://github.com/Rhythmicc/gpu-ssh-monitor/releases/latest/download"
curl -fLO "${monitor_release_base}/hub-nodes.example.json"
curl -fLO "${monitor_release_base}/gpu-ssh-monitor-hub.service"

sudo install -o root -g root -m 0600 \
  hub-nodes.example.json \
  /etc/gpu-ssh-monitor/nodes.json
sudo editor /etc/gpu-ssh-monitor/nodes.json
```

配置格式如下：

```json
{
  "name": "Machine Hub",
  "refresh_seconds": 1,
  "nodes": [
    {
      "name": "training-1",
      "profile": "gpu",
      "description": "Training node",
      "address": "192.0.2.10:23234",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    },
    {
      "name": "storage-1",
      "profile": "nas",
      "description": "Storage and services",
      "address": "192.0.2.20:23234",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    }
  ]
}
```

`host_key` 用于防止 Hub 连接到被冒充的节点。节点重新生成 SSH host key 后，需要同步更新这里的指纹。

### 安装 Hub 服务

Hub 默认监听 23235，可以和本机的 23234 节点监控服务共存：

```bash
sudo install -o root -g root -m 0644 \
  gpu-ssh-monitor-hub.service \
  /etc/systemd/system/gpu-ssh-monitor-hub.service
sudo systemctl daemon-reload
sudo systemctl enable --now gpu-ssh-monitor-hub.service
sudo systemctl status gpu-ssh-monitor-hub.service
```

连接 Hub：

```bash
ssh -p 23235 monitor@hub-host
```

| 按键 | 功能 |
| --- | --- |
| `↑` / `↓` / `←` / `→` | 选择服务器 |
| `Enter` | 打开所选服务器 |
| `Esc` | 从服务器详情返回 Hub 首页 |
| `r` | 立即刷新所有服务器 |
| `t` | 切换当前会话的深色、浅色主题 |
| `q` | 退出 |

首页和服务器卡片均支持鼠标点击。

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
| `MACHINE_CONFIG_FILE` | 空 | 节点角色、网卡、挂载点及服务检查 JSON 配置 |
| `HUB_NODES_FILE` | 空 | Hub 节点 JSON 配置；设置后首页切换为多服务器模式 |
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
- Hub 节点离线：确认 Hub 能访问节点的 TCP 23234 端口，并检查 `host_key` 指纹；
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

安装了 Hub 时，先执行 `sudo systemctl disable --now gpu-ssh-monitor-hub.service`，并删除对应的 systemd 单元；`nodes.json` 中只包含节点地址和 host key 指纹，可以按需要保留或删除。
