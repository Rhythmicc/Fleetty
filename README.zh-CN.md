[English](README.md) | 简体中文

<div align="center">

# Fleetty

一台机器，或一整组机器，都在终端里。

[![CI](https://img.shields.io/github/actions/workflow/status/Rhythmicc/Fleetty/ci.yml?branch=main&label=CI)](https://github.com/Rhythmicc/Fleetty/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Rhythmicc/Fleetty)](https://github.com/Rhythmicc/Fleetty/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/Rhythmicc/Fleetty)](go.mod)
[![License](https://img.shields.io/github/license/Rhythmicc/Fleetty)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/Rhythmicc/fleetty)](https://goreportcard.com/report/github.com/Rhythmicc/fleetty)

<img src="docs/images/fleetty-hero.webp" alt="Fleetty 终端集群监控" width="100%">

</div>

Fleetty 是一个 Go 二进制。在你面前的机器上，它像 `top`：CPU、内存、磁盘、网络、进程，以及硬件确实能报出数据时的 GPU。指向一台服务器时，同一块界面由 Fleetty 自己的 SSH 端口提供。这个端口不开放 shell。监控保持只读，直到有人为管理操作另行认证。每个会话各自保留页面、排序和主题。

## 功能

- **本地 top。** Linux 和 macOS 上的逐核负载、频率、内存、磁盘、网络和进程。
- **GPU。** Linux 上的 NVIDIA，macOS 上的 Apple Silicon。只有采到样本才出现对应卡片。
- **Hub。** 在一屏上看多台机器的负载、温度、流量和地址。Enter 进入节点。离线节点保留最后在线时间，并自行重试。
- **Slurm。** 同时看多个集群，包括某台节点上的作业和下一份合资格作业。窄终端仍保留用户、已运行时间和作业名。
- **存储地图。** 只在本地 `fleetty top` 中浏览占用、查重、压缩和删除。每次写操作都要确认。
- **服务。** NAS profile 下的挂载点、Docker、PM2 和 HTTP 检查。`fleettyctl` 用一份清单安装和更新多台机器，包括经过登录节点才能到达的节点。
- **主题。** 深色和浅色。`t` 切换当前会话。默认的 `auto` 跟随终端，本地和 SSH 都一样，不必转发主题变量。仓库里的 [Fleetty Dark](themes/ghostty/Fleetty%20Dark) 和 [Fleetty Light](themes/ghostty/Fleetty%20Light) 是可选的 [Ghostty](https://ghostty.org/) 客户端主题。见 [Ghostty 与主题](docs/zh-CN/themes.md)。

## 快速开始

### 安装

支持 Linux 和 macOS 的 `amd64`、`arm64`。脚本会用发布包里的 `checksums.txt` 校验下载内容。

```bash
curl -fsSL https://raw.githubusercontent.com/Rhythmicc/Fleetty/main/install.sh | sh
```

`FLEETTY_VERSION` 选择版本标签，默认 `latest`。`FLEETTY_INSTALL_DIR` 选择安装目录，默认 `~/.local/bin`。该目录不在 `PATH` 中时，脚本会提示。

已有 Go 工具链时，版本以 [go.mod](go.mod) 为准：

```bash
go install github.com/Rhythmicc/fleetty/cmd/fleetty@latest
```

也可以从 [Releases](https://github.com/Rhythmicc/Fleetty/releases) 下载 `fleetty_linux_amd64`、`fleetty_linux_arm64`、`fleetty_darwin_amd64` 或 `fleetty_darwin_arm64`，并与同一发布中的 `checksums.txt` 对照。

### 在本地运行

```bash
fleetty top
```

本地监控不需要 root，也不监听端口。终端可交互时，不带参数的 `fleetty` 会打开启动菜单。只有执行 `fleetty serve`，或在菜单里选择 SSH Server，才会启动 SSH 服务。

### 打开远程仪表盘

先在服务器上安装，登记客户端公钥并启动服务，步骤见 [安装 Linux 节点](docs/zh-CN/installation.md#linux-ssh-节点)。然后：

```bash
ssh -p 23234 monitor@node.example    # one node
ssh -p 23235 monitor@hub.example     # a Hub
```

SSH 用户名只用来标记会话，并不是登录某个系统账户。服务只接受 `authorized_keys` 里的公钥。只读监控不询问管理密码。

- [安装 Linux 节点：普通用户或系统服务](docs/zh-CN/installation.md#linux-ssh-节点)
- [配置 Hub](docs/zh-CN/hub.md)
- [接入 Slurm 队列](docs/zh-CN/slurm.md)
- [由 Hub 更新节点，包括登录节点后面的节点](docs/zh-CN/deployment.md#由-hub-自动更新节点)

## 配置

`fleetty top` 不需要配置文件。下面的标志都是可选的：

```bash
fleetty top --config machine.json --theme auto --layout layout.json
```

`--theme` 取 `auto`、`dark` 或 `light`，并覆盖 `DEFAULT_THEME`。`--layout` 覆盖 `FLEETTY_LAYOUT_FILE`。两者都未设置时，布局文件是用户配置目录下的 `fleetty/layout.json`：Linux 为 `~/.config`（或 `$XDG_CONFIG_HOME`），macOS 为 `~/Library/Application Support`。

机器配置是 JSON。一台 NAS 可以写成：

```json
{
  "name": "storage-01",
  "profile": "nas",
  "network_interfaces": ["bond0"],
  "mounts": ["/", "/data/shared"],
  "docker": true,
  "pm2_user": "alice",
  "http_checks": [{ "name": "Files", "url": "https://files.example/" }]
}
```

`profile` 为 `gpu`、`cpu`、`nas` 或 `general`。`storage` 即 `nas`，`server` 即 `general`。macOS 上不带文件的 `fleetty top` 使用 `general`。没有文件的 Linux 服务使用 `gpu`。卡片仍然只在采集成功时出现。

用户安装把二进制放在 `~/.local/bin/fleetty`，配置放在 `~/.config/fleetty`，并使用 systemd 用户单元。系统安装使用 `/opt/fleetty` 和 `/etc/fleetty`。节点监听 `0.0.0.0:23234`。Hub 监听 `23235`。

设置了 `MACHINE_CONFIG_FILE` 时，历史记录是机器文件旁边的 `history.jsonl`，否则是 `~/.config/fleetty/history.jsonl`。`FLEETTY_HISTORY_FILE` 可以指定其他路径。Hub 读取 `HUB_NODES_FILE`。

包括密钥、超时和管理密码在内的全部配置项见 [配置与故障排查](docs/zh-CN/configuration.md)。

## 使用

在节点仪表盘上：

| 按键 | 功能 |
| --- | --- |
| `1` / `2` / `3` | Overview / Compute / Network |
| `4` | 存储地图。仅本地 `fleetty top` |
| `Tab` / `Shift+Tab` | 下一页 / 上一页。左右方向键相同，存储地图除外 |
| `↑` / `↓`、单击 | 选择进程 |
| `Enter` / `Esc` | 进程详情 / 返回 |
| `/` / `c` | 过滤 / 清除过滤 |
| `o` / `O` | 下一个排序字段 / 反转顺序 |
| `l` | 布局编辑器。NAS profile 没有自定义布局 |
| `m` | 管理模式。需要已设置 `ADMIN_PASSWORD_HASH` |
| `t` | 为当前会话切换深色和浅色 |
| `r` / `q` | 刷新 / 退出 |

Compute 页在 32 个逻辑 CPU 及以下为每个核画一条横条，超过 32 个则使用随终端变宽的柱图，历史图放在右侧窄栏。`f` 聚焦某个核。`v` 在柱图和完整逐核列表之间切换。这个选择只在当前会话有效。

在 Linux 上，Network 页按实时 `DOWN + UP` 排列 TCP 连接，并显示所属用户。内核没有提供的计数保持空白，不会画成零。

存储地图只在本地 `fleetty top` 中提供。`d` 删除，`z` 先写成 `.7z` 再删除原文件。两者分别要求输入 `DELETE` 或 `ARCHIVE`。`u` 扫描重复文件。在该视图里，`c` 选择要保留的那一份。远程会话不能删除文件。`q` 和 `Esc` 返回上一级目录；在扫描根目录时则退出。

在 Hub 上，方向键在服务器之间移动，Enter 打开选中的服务器，配置了 Slurm 时 `s` 打开队列。`t` 和 `r` 是主题和刷新。在队列里按 `q` 回到服务器列表；在服务器列表里按 `q` 退出。

[完整操作说明](docs/zh-CN/usage.md) · [管理模式可以做什么](docs/zh-CN/security.md)

## 截图

这些画面是真实 TUI 用一套虚构演示数据画出来的。用 `sh docs/demo/render.sh` 可以重新生成。

<a href="docs/images/fleetty-hub.webp">
  <img src="docs/images/fleetty-hub.webp" alt="Fleetty 中的 Acme Research Cluster，含一台离线节点和一块高温 GPU" width="100%">
</a>

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>GPU 节点</strong><br>
      主机、GPU、该节点的 Slurm 队列和进程。<br><br>
      <a href="docs/images/fleetty-gpu.webp">
        <img src="docs/images/fleetty-gpu.webp" alt="虚构节点 gpu-a01 的 Fleetty 总览">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>存储节点</strong><br>
      挂载点、Docker、PM2 和 HTTP 检查，其中有一项不健康。<br><br>
      <a href="docs/images/fleetty-nas.webp">
        <img src="docs/images/fleetty-nas.webp" alt="虚构节点 storage-01 的 Fleetty 总览">
      </a>
    </td>
  </tr>
</table>

### 主题

`t` 在当前会话的深色和浅色之间切换。下面是同一份 Hub 数据的两种主题。

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>深色</strong><br><br>
      <a href="docs/images/fleetty-hub.webp">
        <img src="docs/images/fleetty-hub.webp" alt="深色主题下的 Fleetty Hub">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>浅色</strong><br><br>
      <a href="docs/images/fleetty-hub-light.webp">
        <img src="docs/images/fleetty-hub-light.webp" alt="浅色主题下的 Fleetty Hub">
      </a>
    </td>
  </tr>
</table>

<img src="docs/images/fleetty-themes.gif" alt="Fleetty Hub 在深色和浅色主题之间切换" width="100%">

## 文档

| 主题 | 文档 |
| --- | --- |
| 平台要求、安装、自更新与卸载 | [安装与升级](docs/zh-CN/installation.md) |
| 页面、逐核 CPU、频率、网络与存储地图 | [使用指南](docs/zh-CN/usage.md) |
| 终端主题、自动跟随与 prompt 配色 | [Ghostty 与主题](docs/zh-CN/themes.md) |
| 多服务器，以及节点如何认证 Hub | [Hub](docs/zh-CN/hub.md) |
| 多集群、作业状态、QOS 与窄屏队列 | [Slurm](docs/zh-CN/slurm.md) |
| Docker、PM2、挂载点与 HTTP 检查 | [NAS 与服务监控](docs/zh-CN/nas.md) |
| 幂等安装、fleet manifest 与分层更新 | [部署与运维](docs/zh-CN/deployment.md) |
| 管理密码、公钥和特权助手 | [认证与管理](docs/zh-CN/security.md) |
| 配置项、服务日志与常见问题 | [配置与故障排查](docs/zh-CN/configuration.md) |
| 采集频率、连接复用与历史数据 | [采集与性能](docs/zh-CN/performance.md) |

## 参与贡献

欢迎报告问题，也欢迎拉取请求。见 [CONTRIBUTING.zh-CN.md](CONTRIBUTING.zh-CN.md)。标志、配置项或可见行为有变化时，请在同一个拉取请求里同时更新英文和中文文档。

```bash
go test -race ./...
go vet ./...
go build -o fleetty ./cmd/fleetty
go build -o fleettyctl ./cmd/fleettyctl
```

Go 版本以 [go.mod](go.mod) 为准。

## 许可证

[MIT](LICENSE)
