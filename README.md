# GPU SSH Monitor

GPU SSH Monitor 是一个面向计算服务器和存储节点的 SSH 监控界面。连接专用 SSH 端口后会直接进入终端仪表盘，不会获得服务器 shell。

它以单一可执行文件运行，适合为运维人员或服务器使用者提供统一、受控的主机状态入口。

## 功能概览

- CPU 使用率及 1/5/15 分钟负载；
- 内存与根磁盘容量、使用率；
- 网络实时收发速率、累计流量和最近 60 秒趋势；
- NVIDIA GPU 利用率、显存、核心频率、温度和功率；
- 按 CPU 排序的进程列表、状态颜色、过滤和只读详情；
- 随终端尺寸自动调整的响应式布局；
- 每个 SSH 会话独立的深色、浅色主题；
- 鼠标和键盘操作；
- 密码保护的管理模式；
- 可选的多服务器 Hub 首页；
- 可接入多个本地或远程 Slurm 集群的队列页面；
- 面向 NAS 的网络、存储、Docker 和 HTTP 服务监控页面。

普通监控界面可以过滤进程并查看只读详情，不需要管理密码。管理模式额外提供发送 `SIGTERM`、重启监控服务和重启主机等写操作。所有危险操作都需要再次确认，PID 1 和监控程序自身不能从界面终止。

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

# 允许当前操作者的 SSH 公钥连接监控端口。
# 如需允许多人访问，可将多行公钥写入同一个文件。
sudo install -o root -g root -m 0600 \
  "$HOME/.ssh/id_ed25519.pub" \
  /etc/gpu-ssh-monitor/authorized_keys

sudo systemctl daemon-reload
sudo systemctl enable --now gpu-ssh-monitor.service
sudo systemctl status gpu-ssh-monitor.service
```

服务默认监听 `0.0.0.0:23234`，但只接受 `/etc/gpu-ssh-monitor/authorized_keys` 中登记的客户端公钥。首次启动时会自动创建 `/etc/gpu-ssh-monitor/ssh_host_ed25519`，请勿在升级时删除该文件，否则 SSH host key 会发生变化。

如果服务器启用了防火墙，请只向需要访问监控的网络开放 TCP 23234 端口。

## 连接与操作

监控端口不会登录系统账户，SSH 用户名只用于标记会话来源；客户端仍需持有已登记的私钥，连接后不会获得 shell：

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

只读监控页面不要求管理密码，但 SSH 连接本身必须通过公钥认证。只读进程详情会隐藏工作目录，并自动遮盖密码、令牌、API Key 等常见敏感参数；通过管理密码认证后才会显示完整详情。

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

将节点配置为 `nas` 后，默认页面会改为存储服务器视图，重点展示指定物理网卡的实时吞吐、累计流量、错误与丢包，各挂载点的容量告警，以及 Docker、PM2 和 HTTP 服务的健康状态。Docker 表格包含镜像、健康检查、CPU、内存、网络 I/O、进程数、重启次数、运行时间和端口；PM2 表格包含应用状态、实例 ID、PID、CPU、内存、运行时间、重启次数和执行模式。管理模式仍然可以查看和终止进程、重启监控服务或重启主机。

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
  "pm2_user": "service-account",
  "http_checks": [
    {"name": "Web console", "url": "http://127.0.0.1/"},
    {"name": "Metrics", "url": "http://127.0.0.1:3000/"}
  ]
}
```

服务以 root 运行时会通过本机 Docker socket 只读采集容器状态和资源数据。设置 `pm2_user` 后，监控程序会查找该用户已经运行的 PM2 daemon，并通过 `pm2 jlist` 读取应用状态；它不会启动 PM2 daemon，也不会修改或重启应用。HTTP 检查由节点本机发起，因此可以检查只监听 `127.0.0.1` 的服务。请只配置可信 URL；监控程序不会读取完整响应正文，也不会显示或保存容器及 PM2 应用的环境变量。

## 多服务器 Hub

Hub 使用同一个 Go 可执行文件运行，并通过各节点现有的 23234 SSH 端口读取状态。连接 Hub 后会先看到所有服务器的简报，节点会根据 `profile` 自动分到 GPU 计算、NAS 与存储等区域；选择服务器即可进入该节点的完整监控和管理界面。Hub 名称由配置文件中的 `name` 决定，可以为不同实验室或集群分别命名。

Hub 不需要系统 SSH 账户，也不会在磁盘中保存节点管理密码。进入某个节点的管理模式时，密码会通过加密的 SSH 连接发送给该节点即时校验，并且只保留在当前 Hub 会话的内存中。

Hub 使用独立的 RPC 密钥连接节点。该密钥不能用于打开交互监控页面，普通操作者的密钥也不能冒充 Hub RPC 身份。

关机或暂时不可达的节点会显示为 `OFFLINE`，不会阻塞其他节点的每秒刷新。Hub 会逐步降低离线节点的探测频率，并在节点恢复后自动重新上线；也可以选中节点后按 `r` 立即重试。

部署 Hub 前，应先将各监控节点升级到相同版本。

### 创建节点配置

先在 Hub 主机上创建专用 RPC 密钥：

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/gpu-ssh-monitor/node_rpc_ed25519
sudo chmod 0600 /etc/gpu-ssh-monitor/node_rpc_ed25519
```

将 `/etc/gpu-ssh-monitor/node_rpc_ed25519.pub` 的内容安装到每个节点的 `/etc/gpu-ssh-monitor/hub_authorized_keys`，权限设为 `0600`。然后在每个节点的 `/etc/gpu-ssh-monitor/machine.env` 中加入：

```bash
NODE_RPC_AUTHORIZED_KEYS_FILE=/etc/gpu-ssh-monitor/hub_authorized_keys
```

更新配置后重启节点的 `gpu-ssh-monitor.service`。节点只会允许这组公钥以内部用户 `gpu-monitor-hub` 调用受限 RPC，不会提供 shell。

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
      "slurm_cluster": "Local GPU Cluster",
      "slurm_node": "gpu01",
      "identity_file": "/etc/gpu-ssh-monitor/node_rpc_ed25519",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    },
    {
      "name": "storage-1",
      "profile": "nas",
      "description": "Storage and services",
      "address": "192.0.2.20:23234",
      "identity_file": "/etc/gpu-ssh-monitor/node_rpc_ed25519",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    }
  ],
  "slurm_clusters": [
    {
      "name": "Local GPU Cluster",
      "description": "Slurm available on the Hub host",
      "transport": "local"
    },
    {
      "name": "Remote GPU Cluster",
      "description": "Slurm reached through a login node",
      "transport": "ssh",
      "address": "login.example.com:22",
      "user": "slurm-monitor",
      "identity_file": "/etc/gpu-ssh-monitor/slurm_remote_ed25519",
      "host_keys": [
        "SHA256:replace-with-the-ed25519-host-key-fingerprint",
        "SHA256:replace-with-the-ecdsa-host-key-fingerprint"
      ],
      "partitions": ["gpu"],
      "refresh_seconds": 2,
      "timeout_seconds": 5
    }
  ]
}
```

`identity_file` 用于证明 Hub 身份，私钥必须由 root 所有且权限不超过 `0600`。`host_key` 用于防止 Hub 连接到被冒充的节点。节点重新生成 SSH host key 后，需要同步更新这里的指纹。

隔离网络中的旧节点可以在滚动迁移期间临时设置顶层字段 `"insecure_allow_unauthenticated_nodes": true`。该选项会降低 Hub 到节点的身份保证，不应作为长期配置；全部节点安装 RPC 公钥后应立即删除。

### Slurm 队列

Hub 可以同时读取多个互相独立的 Slurm 集群。每个集群对应一个 `slurm_clusters` 条目，计算节点名称、分区名称和登录节点位置均由配置决定。

`transport` 支持两种模式：

| 模式 | 适用场景 |
| --- | --- |
| `local` | Hub 就运行在 Slurm 登录节点上，或本机可以直接执行 `sinfo` 和 `squeue` |
| `ssh` | Slurm 位于另一套网络或登录节点后，Hub 使用只读 SSH 密钥远程执行 `sinfo` 和 `squeue` |

远程模式建议使用专用的低权限系统账户和独立密钥。私钥只需对 Hub 服务的 `root` 用户可读：

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/gpu-ssh-monitor/slurm_remote_ed25519
sudo chmod 0600 /etc/gpu-ssh-monitor/slurm_remote_ed25519
```

将生成的公钥加入远程登录节点上 `slurm-monitor` 用户的 `authorized_keys`，建议在公钥前添加 `restrict` 以禁用端口转发、PTY 和代理转发，并限制该账户的 sudo 权限。取得登录节点提供的 Host Key 指纹：

```bash
ssh-keyscan -p 22 login.example.com 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

将输出中的全部有效 `SHA256:` 指纹写入 `host_keys`，以兼容登录节点同时发布 Ed25519、ECDSA 或 RSA Host Key 的情况。只登记一个指纹时，也可以继续使用单值字段 `host_key`。

可选的 `partitions` 只展示指定分区；省略时展示该集群的全部分区和作业。`refresh_seconds` 默认 2 秒，`timeout_seconds` 默认 4 秒。某个 Slurm 源离线时，Hub 会保留最后一次状态并单独退避重试，不影响服务器首页或其他集群。

计算节点可以通过 `slurm_cluster` 和 `slurm_node` 关联到队列数据。`slurm_cluster` 必须与某个集群的 `name` 完全相同，`slurm_node` 使用 `sinfo -N` 显示的节点名：

```json
{
  "name": "training-1",
  "profile": "gpu",
  "address": "192.0.2.10:23234",
  "identity_file": "/etc/gpu-ssh-monitor/node_rpc_ed25519",
  "host_key": "SHA256:replace-with-the-node-host-key-fingerprint",
  "slurm_cluster": "Local GPU Cluster",
  "slurm_node": "gpu01"
}
```

建立关联后，从 Hub 打开的计算节点面板会显示有限条作业：当前节点正在运行的作业、调度顺序中最靠前且分区包含该节点的候选作业，以及少量后续排队作业。独立 Slurm 页面使用相同的颜色语义：

| 状态 | 含义 |
| --- | --- |
| `RUNNING` | 已分配到该节点或集群中正在运行 |
| `NEXT` | 当前队列顺序中最靠前的候选作业 |
| `QUEUED` | 其余等待调度的作业 |

`NEXT` 表示 Slurm `squeue` 返回顺序中的首个合资格 pending 作业，是便于观察的候选提示；最终调度仍由 Slurm 的优先级、资源、依赖和 backfill 策略决定。

### 安装 Hub 服务

Hub 默认监听 23235，可以和本机的 23234 节点监控服务共存：

```bash
sudo install -o root -g root -m 0600 \
  "$HOME/.ssh/id_ed25519.pub" \
  /etc/gpu-ssh-monitor/authorized_keys
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
| `s` | 打开 Slurm 队列 |
| `Tab` | 在服务器与 Slurm 页面之间切换 |
| `←` / `→` | 在 Slurm 页面按集群过滤 |
| `a` | 在 Slurm 页面恢复显示全部集群 |
| `r` | 立即刷新服务器和 Slurm 队列 |
| `t` | 切换当前会话的深色、浅色主题 |
| `q` | 在 Slurm 队列返回 Hub 首页；在 Hub 首页退出 |

首页和服务器卡片均支持鼠标点击。

在 GPU 计算节点详情中，`NODE QUEUE` 默认优先获得更多高度。布局调整只影响当前 SSH 会话，不会改变其他用户的界面：

| 按键 | 功能 |
| --- | --- |
| `Tab` | 在 `NODE QUEUE` 与 `PROCESSES` 之间切换焦点 |
| `+` / `-` | 增加或减少当前模块的高度 |
| `↑` / `↓` | 滚动队列，或选择进程 |
| `Enter` / 鼠标单击 | 查看所选进程的只读详情 |
| `/` | 按进程名、用户或 PID 过滤 |
| `c` | 清除进程过滤条件 |

只读详情不会提供终止入口。发送信号及其他主机写操作仍然只能在通过密码认证的管理模式中执行。

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
| `SSH_AUTHORIZED_KEYS_FILE` | systemd 服务中为 `/etc/gpu-ssh-monitor/authorized_keys` | 允许连接交互 TUI 的客户端公钥 |
| `NODE_RPC_AUTHORIZED_KEYS_FILE` | 空 | 允许以内部 Hub 身份连接节点的公钥 |
| `SSH_ALLOW_ANONYMOUS` | `false` | 仅用于隔离网络迁移；显式设为 `true` 才允许匿名连接 |
| `SSH_MAX_CONNECTIONS` | `64` | 同时接受的 SSH 连接上限 |
| `SSH_IDLE_TIMEOUT` | `30m` | SSH 空闲连接超时 |
| `SSH_MAX_TIMEOUT` | `24h` | 单个 SSH 连接的最长持续时间 |
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

首次从允许匿名连接的旧版本升级时，必须先安装操作者公钥，并确保 systemd 设置了 `SSH_AUTHORIZED_KEYS_FILE`：

```bash
sudo install -o root -g root -m 0600 \
  "$HOME/.ssh/id_ed25519.pub" \
  /etc/gpu-ssh-monitor/authorized_keys
sudo install -d -o root -g root -m 0755 \
  /etc/systemd/system/gpu-ssh-monitor.service.d
printf '%s\n' \
  '[Service]' \
  'Environment=SSH_AUTHORIZED_KEYS_FILE=/etc/gpu-ssh-monitor/authorized_keys' |
  sudo tee /etc/systemd/system/gpu-ssh-monitor.service.d/security.conf >/dev/null
```

如果节点由 Hub 管理，还应先完成“创建节点配置”中的 RPC 密钥部署，并在节点设置 `NODE_RPC_AUTHORIZED_KEYS_FILE`。完成认证配置后，再执行下载、校验和二进制替换：

```bash
sudo install -o root -g root -m 0755 \
  "/tmp/gpu-ssh-monitor_linux_${monitor_arch}" \
  /opt/gpu-ssh-monitor/gpu-ssh-monitor
sudo systemctl daemon-reload
sudo systemctl restart gpu-ssh-monitor.service
```

Hub 服务需要同样为 23235 端口配置 `SSH_AUTHORIZED_KEYS_FILE`。升级不会覆盖 `/etc/gpu-ssh-monitor` 中的客户端公钥、RPC 密钥、管理配置和 SSH host key。

## 故障排查

查看服务状态和最近日志：

```bash
sudo systemctl status gpu-ssh-monitor.service
sudo journalctl -u gpu-ssh-monitor.service -n 100 --no-pager
```

- 无法连接：检查服务状态、TCP 23234 端口、防火墙规则和客户端公钥是否存在于 `authorized_keys`；
- GPU 区域不可用：确认 `nvidia-smi` 能以 root 身份正常执行；
- 管理模式不可用：检查 `/etc/gpu-ssh-monitor/admin.env` 的权限和 `ADMIN_PASSWORD_HASH`；
- Hub 节点离线：确认 Hub 能访问节点的 TCP 23234 端口，并检查 `identity_file`、节点的 `hub_authorized_keys` 和 `host_key` 指纹；
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
