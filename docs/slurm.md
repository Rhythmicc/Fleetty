# Slurm 队列

[返回 README](../README.md)

Hub 可以同时读取多个互相独立的 Slurm 集群。每个集群对应一个 `slurm_clusters` 条目，计算节点名称、分区名称和登录节点位置均由配置决定。

`transport` 支持两种模式：

| 模式 | 适用场景 |
| --- | --- |
| `local` | Hub 就运行在 Slurm 登录节点上，或本机可以直接执行 `sinfo` 和 `squeue` |
| `ssh` | Slurm 位于另一套网络或登录节点后，Hub 使用只读 SSH 密钥远程执行 `sinfo` 和 `squeue` |

远程模式建议使用专用的低权限系统账户和独立密钥。私钥只需对 Hub 服务的 `root` 用户可读：

```bash
sudo ssh-keygen -t ed25519 -N "" \
  -f /etc/fleetty/slurm_remote_ed25519
sudo chmod 0600 /etc/fleetty/slurm_remote_ed25519
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
  "group": "gpu-compute",
  "profile": "gpu",
  "address": "192.0.2.10:23234",
  "identity_file": "/etc/fleetty/node_rpc_ed25519",
  "host_key": "SHA256:replace-with-the-node-host-key-fingerprint",
  "slurm_cluster": "Local GPU Cluster",
  "slurm_node": "gpu01"
}
```

建立关联后，从 Hub 打开的计算节点面板会显示有限条作业：当前节点正在运行的作业、调度顺序中最靠前且分区包含该节点的候选作业，以及少量后续排队作业。独立 Slurm 页面使用相同的颜色语义：

| 状态 | 含义 |
| --- | --- |
| `RUNNING` | 已分配到该节点或集群中正在运行 |
| `NEXT` | 当前分区队列中最靠前的合资格候选作业 |
| `QUEUED` | 其余等待调度的作业 |

`NEXT` 按集群和分区分别计算，表示 Slurm `squeue` 返回顺序中该分区的首个合资格 pending 作业。一个集群可以同时有多个 `NEXT`；依赖未满足或受到作业数组并发上限约束的任务不会占用该标记。它是便于观察的候选提示，最终调度仍由 Slurm 的优先级、资源、依赖和 backfill 策略决定。

`WEIGHT` 显示 Slurm `squeue %Q` 返回的原始总优先级。数值越高通常表示调度优先级越高，但它不是开工时间承诺；资源是否满足、依赖关系和 backfill 等策略仍会影响实际顺序。

`QOS` 显示 Slurm `squeue %q` 返回的服务质量名称，便于识别用户在提交任务时选择的优先级、时限或资源策略。

队列表格内容宽度小于 112 列时使用紧凑布局：隐藏 WEIGHT 和独立 QOS 列，在 Job ID 左侧用彩色圆点表示 QOS，依次展示 JOB ID、USER、S、ELAPSED（已运行时间）和 NAME。USER 在窄屏也保留，过长用户名会截断；状态列缩成一位，R 表示 RUNNING、N 表示 NEXT、Q 表示 QUEUED，保留原有状态颜色。宽度足够时再加入 CLUSTER。总队列和节点队列使用相同规则。底部图例标注当前队列的 QOS；normal 为青色、high 为黄色、urgent 为红色、long 为紫色、low/未提供为灰色，其他站点自定义名称使用稳定颜色（不同自定义名称可能同色）。颜色只区分 QOS 名称，不代表 Slurm 的实际优先级排序。总队列按 Enter 查看详情仍可读取完整 QOS 名称及 PRIORITY；宽屏继续展示完整列和状态名称。

队列选中项使用整行背景高亮，包含列间空格和末尾留白；QOS 圆点及状态文字保持原有颜色，不因选中而变灰。

集群配置位于 [Hub 的 nodes.json](hub.md#创建节点配置) 中。
