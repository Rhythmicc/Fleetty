[English](../en/slurm.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# Slurm 队列

一个 Hub 可以读取多套不共享控制器的 Slurm 集群。每个集群是一条 `slurm_clusters` 记录。节点名、分区名和登录节点的位置都来自配置。

## 连接方式

| 传输 | 适用情况 |
| --- | --- |
| `local` | Hub 就在 Slurm 登录节点上，或者本机自己能运行 `sinfo` 和 `squeue` |
| `ssh` | Slurm 在另一套网络里，或在登录节点后面。Hub 用一把只读 SSH 密钥在那里运行 `sinfo` 和 `squeue` |

远程传输应使用专用的低权限账户和单独的密钥。私钥只需要 Hub 服务的 `root` 用户可读：

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/fleetty/slurm_remote_ed25519
sudo chmod 0600 /etc/fleetty/slurm_remote_ed25519
```

把公钥加入远程登录节点上 `slurm-monitor` 的 `authorized_keys`。在密钥前加上 `restrict`，关闭端口转发、PTY 和 agent 转发，并收紧该账户的 sudo。采集登录节点的 host key 指纹：

```bash
ssh-keyscan -p 22 login.example.com 2>/dev/null |
  ssh-keygen -lf - -E sha256
```

把输出里每一个有效的 `SHA256:` 指纹写入 `host_keys`，这样同时发布 Ed25519、ECDSA 和 RSA 的登录节点都能对上。只有一个指纹时，可以继续用单数字段 `host_key`。

可选的 `partitions` 把视图限制在这些分区。省略则显示该集群的全部分区和作业。`refresh_seconds` 默认 2，上限 60。`timeout_seconds` 默认 4，上限 30。[Hub 示例](hub.md#创建节点配置) 把远程超时设为 5，这会覆盖默认值。某个 Slurm 源离线时，Hub 保留最后一次状态，并按自己的退避重试该源。服务器首页和其他集群不受影响。

## 关联计算节点

计算节点用 `slurm_cluster` 和 `slurm_node` 加入队列。`slurm_cluster` 必须等于某个集群的 `name`。`slurm_node` 是 `sinfo -N` 里的节点名：

```json
{
  "name": "training-1",
  "group": "gpu-compute",
  "profile": "gpu",
  "address": "192.0.2.10:23234",
  "identity_file": "/etc/fleetty/node_rpc_ed25519",
  "host_key": "SHA256:replace-with-the-node-host-key-fingerprint",
  "slurm_cluster": "Local GPU Cluster",
  "slurm_node": "gpu01"
}
```

建立关联后，从 Hub 打开的计算节点面板会列出正在该节点运行的作业，以及请求条件与该节点匹配的等待作业。队列使用以下标签：

| 状态 | 含义 |
| --- | --- |
| `RUNNING` | 已分配到该节点，或正在集群中某处运行 |
| `NEXT` | 各目标节点/分区中预计启动时间（`squeue %S`）最早的待调度作业 |
| `PENDING` | 其他等待调度的作业（包含排在更后的预计启动作业，有明确原因时会列出阻塞项） |

`NEXT` 是展示标签，不是 Slurm 原生状态，也不保证绝对启动顺序。对于有多个 backfill 预计时间的同一目标节点或分区，只有最早时间的那一个（或并列最早时间）作业标为 `NEXT`，其余排在后面的作业保持 `PENDING`，其 `squeue %S` 预计时间仍可在作业详情的 `EST. START` 查看；调度器重新评估后，时间可能变化。

`WEIGHT` 是 `squeue %Q` 的原始总优先级。数值越高通常表示调度优先级越高。它不是开工时间的承诺。资源、依赖和 backfill 仍会改变实际顺序。

`QOS` 是 `squeue %q` 的服务质量名称，用来看出提交时选择的优先级、时限或资源策略。

## 队列显示

Hub 队列的内容宽度达到 112 列时，`TIME` 按「已运行时间 / Slurm 运行时限」显示，例如 `4:20 / 30:00`。窄屏表格只显示已运行时间。按 `s` 展开队列，再按 Enter 打开作业详情，可查看两个完整时间。

队列内容宽度小于 112 列时，隐藏 WEIGHT 和独立的 QOS 列，作业号左侧的彩色圆点表示 QOS。紧凑表保留 JOB ID、USER、状态缩写（R 为 RUNNING，N 为 NEXT，P 为 PENDING）、ELAPSED 和 NAME。Hub 从 72 列起在末列 NODE 显示已分配的节点；未分配显示 `—`。图例说明 QOS 颜色，颜色不表示调度顺序。多集群且空间足够时才显示 CLUSTER。同一作业 ID 与状态下的数组子任务会合并为一行展示，格式为 `ID×Count`（`Count == 1` 时仅显示 `ID`）。完整队列按 Enter 可查看各子任务准确作业号、优先级和阻塞原因。

运行中的作业收到有效的 Fleetty socket 进度上报时，作业名旁会显示进度条；空间不足时只显示百分比。上报的 Linux 节点需运行新版 Fleetty，已在 Hub 中关联到 Slurm 集群和节点，并使用数字作业 ID 的 Slurm cgroup。Fleetty 校验作业分配 ID、用户 UID 和实际分配的节点。同一作业有多个上报进程时，表格显示上报数，详情可查看各进程的计数，不合成一个作业百分比。

选中行在整行宽度上高亮，包括列间空隙和行尾空白。选中时 QOS 圆点和状态文字保持自己的颜色。

在作业说明上按 `q` 或 `Esc` 返回队列。再按一次返回 Hub 首页。在首页按 `q` 或 `Esc` 退出。其余 Hub 按键见 [安装 Hub 服务](hub.md#安装-hub-服务)。

集群配置写在 [Hub 的 `nodes.json`](hub.md#创建节点配置) 里。
