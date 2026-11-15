[English](../en/hub.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 多服务器 Hub

Hub 是同一个二进制。它通过各节点已有的 SSH 端口 23234 读取状态。连上之后，第一屏是所有服务器的摘要。分区、顺序，以及节点属于哪一组，都来自 `nodes.json` 的 `groups` 和 `group`。选中一台服务器即可打开它的完整监控和管理界面。Hub 的名称是该文件里的 `name`，两个实验室可以用两个标题。

机架清单一行一台节点。当前读数是 GPU 峰值、整机 CPU 使用率，或 NAS 的收发合计速率 `NET`。旁边的历史条每格对应一个 Hub 刷新周期，最新在最右边；时间窗口等于可见格数乘以刷新间隔。空间足够时，还显示活跃用户和单格内存饼图：`○` 空、`◔` 少量、`◑` 约一半、`◕` 约四分之三、`●` 接近满（87.5% 起），`—` 表示无数据。节点名按上报的最高 GPU 温度着色：低于 60°C 或无读数时保持默认色，达到 60°C、75°C、85°C 时分别显示绿、黄、红。名称前的标记仍独立表示节点健康。宽终端按 `n` 可在详情里查看内存百分比、GPU 温度和告警原因；NAS 详情还保留磁盘用量和服务计数。

Links 侧栏按双向流量之和从高到低展示活跃链路。每行写出两个节点，分列显示 `A → B` 和 `B → A` 的速率，单位是字节每秒。双向均为零的链路不显示；未测到速率的链路单独计数，`≥` 表示采样不完整。按 `n` 切换 Links 和选中节点详情，按 `PgUp` / `PgDn` 翻页。采样结果在会话间共享。只显示对端 IP 能唯一匹配到 `nodes.json` 中节点的连接，不推断别名、回环或共用 IP，不计 Fleetty 监控端口的流量。

窄终端的机架占全宽，按 `n` 可原地切换到可翻页的连接列表，两种视图底部都保留 Slurm 队列。Links 打开时，各节点独立按顶部显示的 Hub 刷新间隔采样。同一节点的请求未结束时不会重叠，服务节点有预留采样名额，不必等待较慢的计算节点。

顶部的 `VIEWERS` 是当前打开的 Hub 交互会话数，包含自己的会话。同一个人打开两个终端算两个，后台采集不计入。

`LIVE` 表示最近一次概览采集成功，不等于节点可以接新任务。`STALE` 单独计数，并保留上次读数。机架历史条使用同一时间轴，点代表缺测，不代表负载为零。`DRAIN`、`DOWN` 等 Slurm 异常显示在节点名旁；完整状态可在指标侧栏查看。调度数据过期时，队列标题统一标记。GPU 百分比表示最高利用率，不表示可调度的卡数。

机架下方保留 Slurm 摘要，空间足够时再显示几条作业。仅在合并查看多个集群的队列时显示 `CLUSTER` 列。按 `s` 在原地展开同一张表，空间允许时上方仍保留当前选中的节点。选中作业后按 `Enter` 查看详情和阻塞原因；`Esc` 先回到列表，再收起队列，节点选择不变。

Hub 不需要系统 SSH 账户，也不会把节点管理密码存到磁盘。进入某个节点的管理模式时，密码通过已经建立的 SSH 会话发给该节点并在那里校验。它只留在当前 Hub 会话的内存里。

Hub 用自己的 RPC 密钥连接节点。这把密钥不能打开交互监控，操作者的密钥也不能冒充 Hub 的 RPC 身份。

Hub 独立采集每台节点，摘要请求最多等 3 秒，不会拖慢其他节点。单次失败会保留最近一次成功读数并标记 `STALE`（数据延迟）；连续 3 次失败才标记 `OFFLINE`。离线后降低探测频率，恢复应答后自动上线。按 `r` 可立即重试。

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
| `↑` / `↓` | 选择服务器；队列展开时选择作业 |
| `Enter` | 打开所选服务器，或查看所选作业的说明 |
| `n` | 切换 Links 和节点详情（窄屏则切换 Links 和机架） |
| `PgUp` / `PgDn` | 队列展开时给作业翻页，否则给集群连线翻页 |
| `Esc` | 返回 Hub 或作业列表、收起已展开队列，或在首页退出 |
| `s` | 在首页展开或收起 Slurm 队列 |
| `←` / `→` | 队列展开时按集群过滤 |
| `a` | 队列展开时显示全部集群 |
| `r` | 立即刷新服务器和 Slurm 队列 |
| `t` | 切换当前会话的深色或浅色主题 |
| `q` | 与 `Esc` 相同：离开作业说明，然后离开队列，在首页退出 |

单击一行选中节点，再点选中行或按 Enter 打开。空间足够时，清单会显示 Hub 配置里的地址，不带端口。

[配置 Slurm 队列](slurm.md) · [由 Hub 推送更新](deployment.md#由-hub-自动更新节点) · [节点页面做什么](usage.md)
