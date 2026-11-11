[English](README.md) | 简体中文

<div align="center">

# Fleetty

**原生支持 SSH 的工作站、GPU 服务器、NAS 存储节点与 Slurm 集群终端监控工具。**

[![CI](https://img.shields.io/github/actions/workflow/status/Rhythmicc/Fleetty/ci.yml?branch=main&label=CI)](https://github.com/Rhythmicc/Fleetty/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Rhythmicc/Fleetty)](https://github.com/Rhythmicc/Fleetty/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Rhythmicc/Fleetty)](go.mod)
[![License](https://img.shields.io/github/license/Rhythmicc/Fleetty)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Rhythmicc/fleetty)](https://goreportcard.com/report/github.com/Rhythmicc/fleetty)

<img src="docs/images/fleetty-hero.webp" alt="Fleetty 终端集群监控" width="100%">

</div>

**Fleetty** 是一个单二进制终端监控控制台，面向本地工作站、GPU 计算节点、存储服务器以及多集群 Slurm 队列。在本地可直接运行 `fleetty top`（无需 root 权限，也不监听任何网络端口）；在服务器上可通过 `fleetty serve` 在专用 SSH 端口（单节点默认 `23234`，多机 Hub 默认 `23235`）提供交互式远程仪表盘。

专用 SSH 端口仅承载 Fleetty 界面与受控内部 RPC，绝不开放系统 shell。交互客户端公钥、Hub RPC 密钥与受密码保护的管理操作彼此隔离；界面布局、排序字段、过滤条件与深浅色主题均按会话独立维护。

## 核心特性

- **主机与逐核 CPU 遥测** — 实时采集整机与逐核 CPU 使用率、工作频率（Linux 读取 `/proc/cpuinfo`；Apple Silicon 无需 root，直接通过 `IOReport` 按 DVFS 档位活跃驻留时间加权计算）、内存、磁盘健康度、电池状态、7 天分钟级持久化历史（`history.jsonl`），以及支持实时排序与过滤的进程表。
- **硬件感知的 GPU 监控** — 支持 Linux NVIDIA GPU（基于 `nvidia-smi`）与 macOS Apple Silicon GPU（基于 `IOKit`，无需 root）。界面按硬件能力自适应：在 Linux 上，若 `nvidia-smi` 调用失败且 PCI 设备清单确认不存在 NVIDIA 显示或 3D 控制器，GPU 区域会自动隐藏并将空间让给相邻面板；若存在 NVIDIA 硬件但采集异常，错误提示会保留在界面上以便排查驱动或权限问题。
- **多服务器 Hub** — 在单一首页（`nodes.json`，默认端口 `23235`）按自定义分组（`gpu`、`cpu`、`network`、`process`）汇总多台机器的负载、内存、温度、上下行速率与主机地址，按 `Enter` 即可通过持久复用的 SSH 连接进入任意节点的完整监控界面。离线节点会保留最后在线时间并按退避策略自动重连，不会阻塞其余节点每秒一次的刷新。
- **多集群 Slurm 队列** — 支持通过本机执行（`local`）或只读 SSH 登录节点传输（`ssh`）同时轮询多套独立的 Slurm 控制器（`sinfo` 与 `squeue`）。集中展示当前节点正在运行的作业、所在分区的首个合资格候选作业（`NEXT`）、调度权重（`WEIGHT`）及彩色编码的 `QOS`。窄终端（宽度 `< 112` 列）自动切换为紧凑表格，完整保留作业号、用户名、状态、已运行时间与作业名称。
- **NAS 与服务健康检查** — 将节点配置为 `"profile": "nas"`（或 `"storage"`）即可重点呈现指定物理网卡的实时吞吐、累计字节、错误与丢包计数、各挂载点容量告警、只读 Docker 容器资源统计、兼容 rootless 部署的 PM2 进程状态（`pm2_user`）以及本机 HTTP 健康探针。
- **本地存储地图与安全去重** — 在本地 `fleetty top` 中按 `4` 打开交互式磁盘空间树状图（Storage Map），支持并发目录扫描（`2`–`8` 个 worker）与有界会话缓存。支持永久删除（`d`）、校验无误后替换为 `.7z` 压缩包（`z`），以及使用并行 SHA-256 扫描 `>= 1 MiB` 的重复文件（`u`）并在 `fleetty/dedupe-plans` 下生成可审阅的符号链接去重脚本。所有写操作均需输入明确确认词（`DELETE` 或 `ARCHIVE`）；远程 SSH 会话始终保持只读，绝不暴露文件删改权限。
- **无 Shell SSH 与分层权限模型** — 基于 Wish 与 Bubble Tea 构建。SSH 用户名仅用于标记会话，不登录操作系统账户。只读进程详情会自动脱敏密码与令牌；输入正确的管理密码（`ADMIN_PASSWORD_HASH` bcrypt 哈希）后方可查看完整详情或执行受控操作。配合可选的加固服务 `fleetty-privileged.service`（通过 `/run/fleetty/privileged.sock` 通信），非特权节点服务也可在严格白名单下重启 `fleetty.service`、重启主机或向启动时间匹配的非 PID 1 进程发送 `SIGTERM`。
- **无头指标导出与集群级滚动更新** — 支持导出单次 JSON 快照（`fleetty snapshot [--processes]`）与 Prometheus 文本指标（`fleetty metrics`、`fleettyctl metrics`）。使用 `fleettyctl` 完成多机配置校验、变更计划、幂等部署与分层更新，支持由 Hub 统一缓存安装包并经由 SSH 登录节点（`relay`）向内网计算节点级联分发，服务启动失败时自动通过 systemd 事务回滚。

## 界面预览

以下截图均来自实际运行中的 Fleetty 会话。

Hub 按分组集中呈现节点在线状态与核心指标；节点离线时保留最后在线时间，并在后台自动重试连接。

<a href="docs/images/fleetty-hub.webp">
  <img src="docs/images/fleetty-hub.webp" alt="Fleetty Hub 总览" width="100%">
</a>

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>GPU 计算节点</strong><br>
      CPU、内存、磁盘、网络、GPU、Slurm 节点队列和进程在同一页。<br><br>
      <a href="docs/images/fleetty-gpu.webp">
        <img src="docs/images/fleetty-gpu.webp" alt="Fleetty GPU 计算节点">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>NAS 与服务节点</strong><br>
      把网络、挂载点、Docker、PM2 和 HTTP 健康状态放在前面。<br><br>
      <a href="docs/images/fleetty-nas.webp">
        <img src="docs/images/fleetty-nas.webp" alt="Fleetty NAS 节点">
      </a>
    </td>
  </tr>
</table>

## 安装

本地监控（`fleetty top`）与集群管理工具（`fleettyctl`）支持 **Linux** 与 **macOS**（`amd64`、`arm64`）。受管 SSH 节点、Hub 与特权助手服务（`fleetty install` 与 `fleetty doctor`）需要带 **`systemd` 的 Linux**（`amd64`、`arm64`）；前台运行 `fleetty serve` 则不依赖 `systemd`。

### 一键安装脚本

```bash
curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
```

### 使用 Go 安装

需使用 [go.mod](go.mod) 中声明的 Go 版本：

```bash
go install github.com/Rhythmicc/fleetty/cmd/fleetty@latest
go install github.com/Rhythmicc/fleetty/cmd/fleettyctl@latest
```

### 预编译二进制（GitHub Releases）

[GitHub Releases](https://github.com/Rhythmicc/Fleetty/releases/latest) 提供各平台预编译二进制（`fleetty_linux_amd64`、`fleetty_linux_arm64`、`fleetty_darwin_amd64`、`fleetty_darwin_arm64` 及对应的 `fleettyctl_*`）、systemd 单元、JSON 配置示例、Ghostty 主题与 `checksums.txt`。以下命令可下载最新正式版、校验 SHA-256 并安装至 `~/.local/bin/fleetty`：

```bash
case "$(uname -s):$(uname -m)" in
  Linux:x86_64)  fleetty_asset=fleetty_linux_amd64 ;;
  Linux:aarch64|Linux:arm64) fleetty_asset=fleetty_linux_arm64 ;;
  Darwin:x86_64) fleetty_asset=fleetty_darwin_amd64 ;;
  Darwin:arm64)  fleetty_asset=fleetty_darwin_arm64 ;;
  *) echo "Unsupported platform" >&2; exit 1 ;;
esac

fleetty_download_dir="$(mktemp -d)"
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fL "$fleetty_release_base/$fleetty_asset" -o "$fleetty_download_dir/$fleetty_asset" &&
curl -fL "$fleetty_release_base/checksums.txt" -o "$fleetty_download_dir/checksums.txt" &&
fleetty_expected="$(awk -v asset="$fleetty_asset" '$2 == asset { print $1 }' "$fleetty_download_dir/checksums.txt")" &&
if command -v sha256sum >/dev/null 2>&1; then
  fleetty_actual="$(sha256sum "$fleetty_download_dir/$fleetty_asset" | awk '{ print $1 }')"
else
  fleetty_actual="$(shasum -a 256 "$fleetty_download_dir/$fleetty_asset" | awk '{ print $1 }')"
fi &&
test -n "$fleetty_expected" && test "$fleetty_expected" = "$fleetty_actual" &&
mkdir -p "$HOME/.local/bin" &&
install -m 0755 "$fleetty_download_dir/$fleetty_asset" "$HOME/.local/bin/fleetty"
```

## 快速开始

### 1. 本地监控

本地监控不需要 root 权限，也不会监听任何网络端口：

```bash
"$HOME/.local/bin/fleetty" top
```

将 `~/.local/bin` 加入 `PATH` 后可直接执行 `fleetty top`（可选传入 `--config PATH`、`--theme auto|dark|light` 或 `--layout PATH`）。不带参数执行 `fleetty` 会打开交互式启动菜单；只有显式运行 `fleetty serve` 或在启动菜单中选择 **SSH Server** 时，才会启动远程 SSH 服务。

### 2. 远程 SSH 节点与多服务器 Hub

在 Linux 服务器上按[安装 Linux 节点](docs/zh-CN/installation.md#linux-ssh-节点)准备好允许连接的客户端公钥，执行原子安装并运行健康检查：

```bash
fleetty_config="$(mktemp -d)"
chmod 0700 "${fleetty_config}"
install -m 0600 ./client.pub "${fleetty_config}/authorized_keys"

"$HOME/.local/bin/fleetty" install --role node --scope user --config-dir "${fleetty_config}"
"$HOME/.local/bin/fleetty" doctor --role node --scope user
```

随后即可从客户端通过 SSH 连接：

```bash
ssh -p 23234 monitor@node-host    # one node
ssh -p 23235 monitor@hub-host     # a Hub of many servers
```

SSH 用户名仅用于标识会话，不会登录系统账户；服务端仅接受 `authorized_keys` 中登记的公钥。只读监控无需输入管理密码，管理与写操作需独立认证。

- [安装 Linux 节点：普通用户范围或系统服务范围](docs/zh-CN/installation.md#linux-ssh-节点)
- [配置多服务器 Hub](docs/zh-CN/hub.md)
- [接入本机或远程登录节点后的 Slurm 队列](docs/zh-CN/slurm.md)
- [由 Hub 统一分发更新，覆盖登录节点后的内网计算节点](docs/zh-CN/deployment.md#由-hub-自动更新节点)

### 3. 无头快照与 Prometheus 指标导出

在本机或基于集群清单导出单次 JSON 快照（含最近 60 分钟历史样本）或 Prometheus 文本指标：

```bash
fleetty snapshot --processes
fleetty metrics --config /etc/fleetty/machine.json
fleettyctl metrics --file fleet.json
```

## 快捷键与操作

### 单节点监控与本地 `top`

| 按键 | 功能 |
| --- | --- |
| `1` / `2` / `3` | 切换至 **Overview** / **Compute** / **Network** 页面 |
| `4` | 切换至本地 **Storage Map**（仅限本地 `fleetty top`） |
| `Tab` / `Shift+Tab` | 下一页 / 上一页 |
| `↑` / `↓`、单击 | 选择进程、节点或目录条目 |
| `Enter` / `Esc` | 打开详情或子目录 / 返回上一级或退出 |
| `/` / `c` | 过滤进程 / 清除过滤（在重复文件视图中 `c` 用于轮换保留副本） |
| `o` / `O`、点击表头 | 切换排序字段（`CPU`、`MEM%`、`RSS`、`ELAPSED`、`PID`、`USER`、`COMMAND`）/ 反转顺序 |
| `f` / `v` | 聚焦单个 CPU 核心 / 在紧凑柱状图与完整逐核列表间切换（`> 32` 个逻辑 CPU） |
| `d` / `z` / `u` / `g` / `G` | 永久删除（`DELETE`）/ 压缩为 `.7z` 后删除（`ARCHIVE`）/ 扫描重复文件 / 生成单组或批量去重脚本 |
| `l` | 打开自定义组件布局编辑器（`K`/`J` 移动、`-`/`+` 调整尺寸、`Space` 显隐、`s` 本地保存） |
| `m` | 进入管理模式（需已配置 `ADMIN_PASSWORD_HASH`） |
| `t` | 切换当前会话主题（`dark` / `light`），并将当前会话由 `auto` 转为手动控制 |
| `r` / `q` | 立即刷新 / 退出 |

### 多服务器 Hub 与 Slurm 视图

| 按键 | 功能 |
| --- | --- |
| `↑` / `↓` / `←` / `→`、单击 | 选择服务器卡片 |
| `Enter` | 打开所选服务器的完整监控界面 |
| `s` / `Tab` | 打开 Slurm 队列 / 在服务器列表与 Slurm 页面之间切换 |
| `←` / `→` / `a` | 在 Slurm 页面按集群过滤 / 显示全部集群 |
| `r` / `t` / `Esc` / `q` | 立即刷新全部节点与队列 / 切换会话主题 / 返回上一级或退出 |

- **自适应逐核 CPU 视图**：在 **Compute** 页面，逻辑 CPU 不超过 32 个时使用逐核水平负载条；超过 32 个时自动切换为最多两排的紧凑纵向柱状图，柱宽随终端宽度自适应，并将历史趋势图置于右侧窄栏。
- **准确的连接级流量归属**：**Network** 页面展示实时速率与累计流量。Linux 上通过 `INET_DIAG` / `TCP_INFO` 采样每条 TCP 连接，按实时 `DOWN + UP` 速率降序排列并标注所属用户；不支持计数或权限受限的套接字显示 `--`，绝不伪造为零。macOS 上通过 `nettop` 采集进程级流量。
- **本地限定的存储写操作**：**Storage Map** 仅在本地 `fleetty top` 中提供。删除、`.7z` 归档与去重脚本生成均带有独立确认，自动跳过网络挂载与虚拟文件系统，且远程 SSH 会话绝不具备文件删改能力。

[完整使用指南](docs/zh-CN/usage.md) · [认证、管理模式与特权助手](docs/zh-CN/security.md)

## Ghostty 与主题

我们推荐使用 [Ghostty](https://ghostty.org/) 作为本地与 SSH 会话的终端客户端。仓库内置了 [Fleetty Dark](themes/ghostty/Fleetty%20Dark)（炭黑背景配柔和亮色）与 [Fleetty Light](themes/ghostty/Fleetty%20Light)（暖纸背景配纯黑光标）两套主题。

在**客户端电脑**上安装主题后，将 Ghostty 配置中的 `theme` 设为：

```ini
theme = dark:Fleetty Dark,light:Fleetty Light
```

Ghostty 会跟随操作系统切换深浅色外观；Fleetty 默认的 `auto` 主题（`--theme auto` 或 `DEFAULT_THEME=auto`）会在启动及运行期间查询并订阅客户端终端的深浅色状态，无论是本地运行还是直接 SSH 连接到监控端口均可自动跟随，无需转发任何环境变量。按 `t` 后，当前会话改为手动锁定主题。

主题安装命令与 Powerlevel10k 提示符配色见 [Ghostty 与主题](docs/zh-CN/themes.md)。建议在 Ghostty 中设置 `background-opacity = 1` 以保持稳定的对比度；字体、快捷键与窗口透明度均由用户自行决定。

## 配置与路径

`fleetty install` 同时支持非特权用户范围（`--scope user`，普通账户默认）与系统范围（`--scope system`，`root` 默认）：

| 项目 | 用户范围（`--scope user`） | 系统范围（`--scope system`） |
| --- | --- | --- |
| 可执行文件 | `~/.local/bin/fleetty` | `/opt/fleetty/fleetty` |
| 配置目录（权限 `0700`） | `~/.config/fleetty` | `/etc/fleetty` |
| systemd 单元目录 | `~/.config/systemd/user` | `/etc/systemd/system` |
| 节点 / Hub 服务单元 | `fleetty.service` / `fleetty-hub.service` | `fleetty.service` / `fleetty-hub.service` |
| SSH 主机密钥 | `ssh_host_ed25519` / `hub_host_ed25519` | `ssh_host_ed25519` / `hub_host_ed25519` |
| 节点 / Hub 配置文件 | `machine.json` / `nodes.json` | `machine.json` / `nodes.json` |
| 客户端 / Hub RPC 授权公钥 | `authorized_keys` / `hub_authorized_keys` | `authorized_keys` / `hub_authorized_keys` |
| 管理模式凭据 | `admin.env`（`ADMIN_PASSWORD_HASH`） | `admin.env`（`ADMIN_PASSWORD_HASH`） |

`fleetty serve` 常用环境变量包括 `SSH_HOST`（默认 `0.0.0.0`）、`SSH_PORT`（节点默认 `23234`，Hub 默认 `23235`）、`SSH_HOST_KEY_PATH`、`SSH_AUTHORIZED_KEYS_FILE`、`NODE_RPC_AUTHORIZED_KEYS_FILE`、`MACHINE_CONFIG_FILE`、`HUB_NODES_FILE`、`FLEETTY_HISTORY_FILE`、`FLEETTY_LAYOUT_FILE`、`DEFAULT_THEME`（`auto` | `dark` | `light`）、`ADMIN_PASSWORD_HASH` 与 `FLEETTY_PRIVILEGED_SOCKET`（`/run/fleetty/privileged.sock`）。完整配置说明与故障排查步骤见 [配置与故障排查](docs/zh-CN/configuration.md)。

## 更新与集群分发

单机可执行文件支持原地自更新，自动完成 SHA-256 校验与新二进制版本自检：

```bash
fleetty update --check             # report only
fleetty update                     # latest stable release
fleetty update --version v0.1.4    # a specific tag
```

`fleetty update` 会在同一文件系统上原子替换当前执行的二进制，并将旧版本保留在备份路径中。更新过程不会修改配置文件或密钥，也不会自动重启正在运行的服务——请在维护窗口自行重启对应的 `systemd` 单元，客户端下次连接时即会使用新版本。

多机批量部署与跨登录节点（`relay`）的分层更新使用 `fleettyctl`：

```bash
fleettyctl validate --file fleet.json
fleettyctl plan     --file fleet.json
fleettyctl apply    --file fleet.json --yes
fleettyctl update   --file /etc/fleetty/fleet-update.json --yes
```

执行 `fleettyctl update` 时，Hub 每个架构只需下载并校验一次安装包，随后写入本地缓存并向直连节点及登录节点后的计算节点分层分发。

[单机安装与升级](docs/zh-CN/installation.md) · [批量部署与定时更新](docs/zh-CN/deployment.md)

## 详细文档

| 主题 | 文档 |
| --- | --- |
| 平台要求、安装、命令行自更新与卸载 | [安装与升级](docs/zh-CN/installation.md) |
| 页面导航、逐核 CPU、频率、网络与存储地图 | [使用指南](docs/zh-CN/usage.md) |
| 终端主题、系统外观自动跟随与 prompt 配色 | [Ghostty 与主题](docs/zh-CN/themes.md) |
| 多服务器汇总与 Hub 到节点的 RPC 认证 | [Hub](docs/zh-CN/hub.md) |
| 多集群轮询、作业状态、QOS 与窄屏紧凑队列 | [Slurm](docs/zh-CN/slurm.md) |
| 物理网卡、挂载点告警、Docker、PM2 与 HTTP 检查 | [NAS 与服务监控](docs/zh-CN/nas.md) |
| 幂等安装器、fleet manifest 与 Hub 分层级联更新 | [部署与运维](docs/zh-CN/deployment.md) |
| 管理密码哈希、SSH 公钥隔离与特权助手 socket | [认证与管理](docs/zh-CN/security.md) |
| 环境变量、systemd 服务日志与常见问题排查 | [配置与故障排查](docs/zh-CN/configuration.md) |
| 采样频率、SSH 连接复用与 7 天 JSONL 历史保留 | [采集与性能](docs/zh-CN/performance.md) |

## 从源码构建

请使用 [go.mod](go.mod) 中声明的 Go 版本。

```bash
go test -race ./...
go vet ./...
go build -o fleetty ./cmd/fleetty
go build -o fleettyctl ./cmd/fleettyctl
```

## 参与贡献

详见 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)。欢迎提交问题报告与拉取请求。当命令行标志、配置键或用户可见行为发生变化时，请在同一个拉取请求中同步更新英文文档（`docs/en/`）与简体中文文档（`docs/zh-CN/`）。

## 许可证

[MIT](LICENSE)
