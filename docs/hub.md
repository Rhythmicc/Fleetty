# 多服务器 Hub

[返回 README](../README.md)

Hub 使用同一个 Go 可执行文件运行，并通过各节点现有的 23234 SSH 端口读取状态。连接 Hub 后会先看到所有服务器的简报；首页分区、顺序和节点归属由 `nodes.json` 的 `groups` 与 `group` 字段明确决定，选择服务器即可进入该节点的完整监控和管理界面。Hub 名称由配置文件中的 `name` 决定，可以为不同实验室或集群分别命名。

节点卡片以 46 个终端字符为目标宽度，按窗口宽度选择最接近的列数并均分可用空间；卡片不窄于 36 个字符，宽屏自动增加列数，满行右侧余白小于列数，不会再因固定宽度留下接近一整张卡片的空白。相邻的小分组共享卡片行，能放进一行的小分组不会跨行拆散。分组标题只覆盖本组卡片占用的宽度，翻页时保留分组上下文。GPU 卡片只显示一次峰值负载，并保留显存、温度、上下行和 IP；GPU 与 CPU 卡片使用整行块状负载条，以填充长度和低负载到高负载的颜色突出忙碌节点。GPU 节点的条形负载取 CPU 使用率与 GPU 峰值使用率中的较高者，CPU 节点使用整机 CPU 使用率。高负载红色表示忙碌，不等同于故障；离线、过热或容量风险仍有独立警示。窄卡片中的容量和速率会压缩小数与单位间空格，但保留 B、KiB、MiB 等单位。

Hub 不需要系统 SSH 账户，也不会在磁盘中保存节点管理密码。进入某个节点的管理模式时，密码会通过加密的 SSH 连接发送给该节点即时校验，并且只保留在当前 Hub 会话的内存中。

Hub 使用独立的 RPC 密钥连接节点。该密钥不能用于打开交互监控页面，普通操作者的密钥也不能冒充 Hub RPC 身份。

关机或暂时不可达的节点会显示为 `OFFLINE`，不会阻塞其他节点的每秒刷新。Hub 会逐步降低离线节点的探测频率，并在节点恢复后自动重新上线；也可以选中节点后按 `r` 立即重试。

部署 Hub 前，应先将各监控节点升级到相同版本。

## 创建节点配置

先在 Hub 主机上创建专用 RPC 密钥：

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/fleetty/node_rpc_ed25519
sudo chmod 0600 /etc/fleetty/node_rpc_ed25519
```

将 `/etc/fleetty/node_rpc_ed25519.pub` 的内容安装到每个节点的 `/etc/fleetty/hub_authorized_keys`，权限设为 `0600`。然后在每个节点的 `/etc/fleetty/machine.env` 中加入：

```bash
NODE_RPC_AUTHORIZED_KEYS_FILE=/etc/fleetty/hub_authorized_keys
```

更新配置后重启节点的 `fleetty.service`。节点只会允许这组公钥以内部用户 `fleetty-hub` 调用受限 RPC，不会提供 shell。

先在准备运行 Hub 的服务器上取得各节点的 SSH host key 指纹，并通过管理员或其他可信通道核对后再填写配置（`ssh-keyscan` 只采集，不验证身份）：

```bash
ssh-keyscan -p 23234 192.0.2.10 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

复制示例配置并填写节点地址与指纹：

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fLO "${fleetty_release_base}/hub-nodes.example.json"
curl -fLO "${fleetty_release_base}/fleetty-hub.service"

sudo install -o root -g root -m 0600 \
  hub-nodes.example.json \
  /etc/fleetty/nodes.json
sudo editor /etc/fleetty/nodes.json
```

配置格式如下：

```json
{
  "version": 1,
  "name": "Fleetty Hub",
  "refresh_seconds": 1,
  "groups": [
    {
      "id": "gpu-compute",
      "title": "GPU COMPUTE",
      "style": "gpu"
    },
    {
      "id": "cluster-services",
      "title": "CLUSTER SERVICES",
      "style": "network"
    }
  ],
  "nodes": [
    {
      "name": "training-1",
      "group": "gpu-compute",
      "profile": "gpu",
      "description": "Training node",
      "address": "192.0.2.10:23234",
      "slurm_cluster": "Local GPU Cluster",
      "slurm_node": "gpu01",
      "identity_file": "/etc/fleetty/node_rpc_ed25519",
      "host_key": "SHA256:replace-with-the-node-host-key-fingerprint"
    },
    {
      "name": "storage-1",
      "group": "cluster-services",
      "profile": "nas",
      "description": "Storage and services",
      "address": "192.0.2.20:23234",
      "identity_file": "/etc/fleetty/node_rpc_ed25519",
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
      "identity_file": "/etc/fleetty/slurm_remote_ed25519",
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

`version` 是 Hub 配置格式的版本，当前必须为 `1`。`groups` 完整定义 Hub 首页的分区：数组顺序就是显示顺序，`title` 是用户可见标题，`style` 可取 `gpu`、`cpu`、`network` 或 `process`，只负责分区的视觉语义。每个节点必须通过 `group` 明确加入一个已声明分区；引用不存在的分区、重复分区、未知字段或旧版本配置都会在服务启动时直接报错，避免默默回退到另一种布局。

节点的 `profile` 与布局分组相互独立：`profile` 决定节点详情页启用 GPU、NAS 等哪类监控能力，`group` 只决定该节点在 Hub 首页出现的位置。因此 NAS、登录节点或其他服务节点可以放在同一个 `CLUSTER SERVICES` 分区，而不会改变各自的详情页。

`identity_file` 用于证明 Hub 身份，私钥必须由 Hub 服务账户可读且权限不超过 `0600`。`host_key` 用于防止 Hub 连接到被冒充的节点。节点重新生成 SSH host key 后，需要同步更新这里的指纹。

隔离网络中的旧节点可以在滚动迁移期间临时设置顶层字段 `"insecure_allow_unauthenticated_nodes": true`。该选项会降低 Hub 到节点的身份保证，不应作为长期配置；全部节点安装 RPC 公钥后应立即删除。

## 安装 Hub 服务

Hub 默认监听 23235，可以和本机的 23234 节点监控服务共存：

```bash
./fleetty_linux_amd64 install \
  --role hub \
  --scope user \
  --config-dir ./hub-config
~/.local/bin/fleetty doctor --role hub --scope user
```

`hub-config` 至少应包含 `authorized_keys` 和 `nodes.json`，以及配置引用的 RPC/Slurm 私钥。system scope 在安装命令前使用 `sudo` 并指定 `--scope system`。

安装器会同时写入独立的 systemd 能力契约。Hub 启动前会验证二进制具备当前配置分组、进程表和固定底栏能力；旧包或不完整构建会在替换服务前被 `fleettyctl` 拒绝，即使手工覆盖了主 unit，持久化的 drop-in 也会阻止不兼容二进制启动。`fleetty version --json` 可以查看版本、提交、源码是否修改以及完整能力列表。

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

每张节点卡片底部显示 Hub 配置中的连接 IP（不带端口）；配置使用主机名时显示 HOST。在线、离线和等待采样时都能查看地址，卡片内不再重复显示打开详情的操作提示。

[配置 Slurm 队列](slurm.md) · [由 Hub 分发更新](deployment.md#由-hub-自动更新节点) · [节点详情页操作](usage.md)
