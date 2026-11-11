[English](../en/hub.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 多服务器 Hub

Hub 是同一个二进制。它通过各节点已有的 SSH 端口 23234 读取状态。连上之后，第一屏是所有服务器的摘要。分区、顺序，以及节点属于哪一组，都来自 `nodes.json` 的 `groups` 和 `group`。选中一台服务器即可打开它的完整监控和管理界面。Hub 的名称是该文件里的 `name`，两个实验室可以用两个标题。

节点卡片以 46 列为目标宽度，选择最接近的列数，再把宽度均分。卡片不会窄于 36 列。宽终端会增加列数。满行右侧剩下的空间小于列数，因此固定宽度不会留下接近一整张卡片的空白。相邻的小组共享一行，能放进一行的分组不会被拆开。分组标题只覆盖本组卡片。翻页时保留分组上下文。GPU 卡片只显示一次峰值负载，并保留显存、温度、两个方向的流量和地址。GPU 与 CPU 卡片使用整行块状负载条，颜色从空闲到忙碌。GPU 节点的条取 CPU 使用率和 GPU 峰值使用率中较高的一个。CPU 节点使用整机 CPU 使用率。红色表示忙碌，不是故障。离线、过热和容量风险有各自的警示。窄卡片会缩短小数和单位前的空格，但仍打印 B、KiB、MiB 等单位。

Hub 不需要系统 SSH 账户，也不会把节点管理密码存到磁盘。进入某个节点的管理模式时，密码通过已经建立的 SSH 会话发给该节点并在那里校验。它只留在当前 Hub 会话的内存里。

Hub 用自己的 RPC 密钥连接节点。这把密钥不能打开交互监控，操作者的密钥也不能冒充 Hub 的 RPC 身份。

关机或暂时不可达的节点显示为 `OFFLINE`，不会拖住其他节点每秒一次的刷新。Hub 会降低对离线节点的探测频率，节点恢复应答后自动上线。选中它按 `r` 可以立刻再试。

部署 Hub 之前，先把各监控节点升级到同一版本。

## 创建节点配置

在 Hub 主机上创建 RPC 密钥：

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/fleetty/node_rpc_ed25519
sudo chmod 0600 /etc/fleetty/node_rpc_ed25519
```

把 `/etc/fleetty/node_rpc_ed25519.pub` 的内容安装到每个节点的 `/etc/fleetty/hub_authorized_keys`，权限 `0600`。在每个节点的 `/etc/fleetty/machine.env` 里加上：

```bash
NODE_RPC_AUTHORIZED_KEYS_FILE=/etc/fleetty/hub_authorized_keys
```

改完后重启节点的 `fleetty.service`。节点允许这组密钥以内部用户 `fleetty-hub` 调用受限 RPC。它不提供 shell。

在将要运行 Hub 的机器上采集每个节点的 SSH host key 指纹，并通过管理员或其他你信任的渠道核对之后再写入。`ssh-keyscan` 只采集，不证明身份：

```bash
ssh-keyscan -p 23234 192.0.2.10 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

复制示例，填上地址和指纹：

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fLO "${fleetty_release_base}/hub-nodes.example.json"
curl -fLO "${fleetty_release_base}/fleetty-hub.service"

sudo install -o root -g root -m 0600 \
  hub-nodes.example.json \
  /etc/fleetty/nodes.json
sudo editor /etc/fleetty/nodes.json
```

文件形如：

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
      "transport": "local",
      "refresh_seconds": 2
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

`version` 是 Hub 配置格式，必须为 `1`。`groups` 完整定义首页。数组顺序就是显示顺序。`title` 是给人看的标题。`style` 取 `gpu`、`cpu`、`network` 或 `process`，只影响分区的外观。每个节点都必须指向一个存在的分组。未知分组、重复分组、未知字段或旧版本配置会在启动时失败，而不是悄悄换成另一种布局。

节点的 `profile` 与分组无关。`profile` 决定节点拿到哪种详情页，例如 GPU 或 NAS。`group` 只决定卡片在首页的位置。NAS、登录节点和其他服务节点可以共用一个 `CLUSTER SERVICES` 分区，而不改变各自的详情页。可用值是 `gpu`、`cpu`、`nas` 和 `general`。`storage` 表示 `nas`，`server` 表示 `general`。省略 profile 时为 `gpu`。

`refresh_seconds` 默认 1，上限 60。上面的远程 Slurm 示例把 `timeout_seconds` 设为 5。省略该字段时，Slurm 的默认超时是 4 秒。见 [Slurm](slurm.md)。

`identity_file` 用来证明 Hub 的身份。私钥必须能被 Hub 服务账户读取，权限不得松于 `0600`。`host_key` 防止 Hub 连到冒充该节点的机器。节点重新生成 SSH host key 后，要在这里更新指纹。

在隔离网络里滚动迁移时，旧节点可以临时使用顶层字段 `"insecure_allow_unauthenticated_nodes": true`。这会削弱 Hub 到节点的身份检查。它不是长期配置。所有节点装上 RPC 公钥后应马上删掉。

用户范围的 Hub 读取 `~/.config/fleetty/nodes.json`。上面的路径属于系统单元。

## 安装 Hub 服务

Hub 默认监听 23235，可以和本机 23234 上的节点监控并存：

```bash
./fleetty_linux_amd64 install \
  --role hub \
  --scope user \
  --config-dir ./hub-config
~/.local/bin/fleetty doctor --role hub --scope user
```

`hub-config` 至少要有 `authorized_keys` 和 `nodes.json`，以及文件引用到的 RPC 或 Slurm 私钥。系统范围加上 `sudo` 和 `--scope system`。系统单元的 host key 是 `/etc/fleetty/hub_host_ed25519`。用户单元使用 `~/.config/fleetty/hub_host_ed25519`。

安装器还会写入 systemd 的能力检查。Hub 启动前会确认二进制具备当前的分组布局、进程表和固定底栏。`fleettyctl` 会在替换服务之前拒绝过旧或不完整的构建。即使有人手动覆盖了主单元，留在磁盘上的 drop-in 也会拒绝不兼容的二进制。`fleetty version --json` 打印版本、提交、源码树是否有未提交修改，以及完整能力列表。

连接：

```bash
ssh -p 23235 monitor@hub-host
```

| 按键 | 功能 |
| --- | --- |
| `↑` / `↓` / `←` / `→` | 选择服务器 |
| `Enter` | 打开所选服务器 |
| `Esc` | 从服务器返回 Hub 首页。从 Slurm 作业说明返回队列。从队列返回首页。在首页退出 |
| `s` | 打开 Slurm 队列 |
| `Tab` | 在配置了集群时，于服务器页和 Slurm 页之间切换 |
| `←` / `→` | 在 Slurm 页按集群过滤 |
| `a` | 在 Slurm 页显示全部集群 |
| `r` | 立即刷新服务器和 Slurm 队列 |
| `t` | 切换当前会话的深色或浅色主题 |
| `q` | 与 `Esc` 相同：离开作业说明，然后离开队列，在首页退出 |

首页和卡片可以点击。

每张卡片底部是 Hub 配置里的地址，不带端口。主机名显示为 HOST。节点在线、离线或仍在等待采样时都能看到地址。卡片不再重复如何打开详情的提示。

[配置 Slurm 队列](slurm.md) · [由 Hub 推送更新](deployment.md#由-hub-自动更新节点) · [节点页面做什么](usage.md)
