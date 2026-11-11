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

建立关联后，从 Hub 打开的计算节点面板会显示一份短列表：正在该节点上运行的作业、分区包含该节点的最早合资格候选，以及排在后面的少量等待作业。独立的 Slurm 页面使用同样的颜色：

| 状态 | 含义 |
| --- | --- |
| `RUNNING` | 已分配到该节点，或正在集群中某处运行 |
| `NEXT` | 该分区队列里最早的合资格候选 |
| `QUEUED` | 其余仍在等待的作业 |

`NEXT` 按集群和分区分别计算。它是 `squeue` 为该分区返回的顺序中，第一个合资格的 pending 作业。一个集群可以同时有多个 `NEXT`。依赖未满足，或受作业数组并发上限约束的作业不会占用这个标记。它是读队列时的提示。真正的决定仍由 Slurm 根据优先级、资源、依赖和 backfill 作出。

`WEIGHT` 是 `squeue %Q` 的原始总优先级。数值越高通常表示调度优先级越高。它不是开工时间的承诺。资源、依赖和 backfill 仍会改变实际顺序。

`QOS` 是 `squeue %q` 的服务质量名称，用来看出提交时选择的优先级、时限或资源策略。

## 队列显示

队列内容宽度小于 112 列时，表格变为紧凑布局。WEIGHT 和独立的 QOS 列会隐藏。作业号左侧的彩色圆点表示 QOS，随后的列是 JOB ID、USER、S、ELAPSED 和 NAME。窄屏仍然保留 USER。过长的用户名会被截断。状态列缩成一个字母：R 表示 RUNNING，N 表示 NEXT，Q 表示 QUEUED，颜色与全称相同。宽度够时再加入 CLUSTER。全集群队列和节点队列使用同一规则。表格下方的图例标出当前队列里的 QOS。`normal` 为青色，`high` 为黄色，`urgent` 为红色，`long` 为紫色，`low` 或缺失为灰色。其他站点自定义名称使用稳定颜色，两个自定义名称可能同色。颜色只区分名称，不是 Slurm 的优先级顺序。在全集群队列上按 Enter 仍会打开包含完整 QOS 名称和 PRIORITY 的详情。宽屏保留完整列和完整状态名。

选中行在整行宽度上高亮，包括列间空隙和行尾空白。选中时 QOS 圆点和状态文字保持自己的颜色。

在作业说明上按 `q` 或 `Esc` 返回队列。再按一次返回 Hub 首页。在首页按 `q` 或 `Esc` 退出。其余 Hub 按键见 [安装 Hub 服务](hub.md#安装-hub-服务)。

集群配置写在 [Hub 的 `nodes.json`](hub.md#创建节点配置) 里。
