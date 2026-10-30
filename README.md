# Fleetty

<p align="center">
  <img src="docs/images/fleetty-hero.webp" alt="Fleetty 统一连接 GPU 计算节点、存储节点和集群队列" width="100%">
</p>

Fleetty 是一个面向个人电脑、计算服务器、存储节点和 Slurm 集群的终端监控控制台。它既可以作为本地 `top` 使用，也可以通过专用 SSH 端口提供不会暴露服务器 shell 的远程仪表盘。

它以单一可执行文件运行，适合为运维人员或服务器使用者提供统一、受控的主机状态入口。

[快速开始](#快速开始) · [Ghostty 主题](#ghostty-与主题) · [操作指南](#常用操作) · [更新](#更新) · [详细文档](#详细文档)

## 能做什么

- **本地 top**：Linux／macOS 的 CPU、逐核负载、工作频率、内存、网络和进程监控。
- **GPU 监控**：Linux NVIDIA GPU 与 Apple Silicon GPU；按硬件能力显示指标。
- **多机 Hub**：统一查看节点负载、温度、网络和 IP，直接进入节点详情。
- **Slurm 队列**：多个集群、节点作业与候选顺序，窄屏保留用户、运行时间和作业名。
- **本地存储地图**：交互式浏览空间占用、查重、压缩和删除，写操作需要明确确认。
- **服务与运维**：NAS、Docker、PM2、HTTP 健康检查，以及安装器、健康检查和分层批量更新。

一个二进制既能本地运行，也能提供专用 SSH 仪表盘。远程连接不开放系统 shell；监控与管理权限分离。界面布局和主题按会话独立。

## 快速开始

### 本地监控

支持 Linux／macOS 的 `amd64`、`arm64`。以下命令下载最新正式版、验证 SHA-256，并安装到当前用户目录：

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

启动本地监控，不需要 root，也不会监听网络端口：

```bash
"$HOME/.local/bin/fleetty" top
```

将 `~/.local/bin` 加入 `PATH` 后可直接使用 `fleetty top`。裸命令 `fleetty` 打开启动菜单，只有显式执行 `fleetty serve` 或选择 SSH Server 才会启动远程服务。

### 远程节点与 Hub

在服务器上按[安装指南](docs/installation.md#linux-ssh-节点)登记客户端公钥并启动服务，再从客户端连接：

```bash
ssh -p 23234 monitor@node-host    # 单节点
ssh -p 23235 monitor@hub-host     # 多服务器 Hub
```

SSH 用户名用于标记会话，并不登录系统账户。服务只接受登记的公钥；普通监控无需管理密码，写操作另行认证。

- [安装 Linux 节点：普通用户或系统服务](docs/installation.md#linux-ssh-节点)
- [配置多服务器 Hub](docs/hub.md)
- [接入 Slurm 队列](docs/slurm.md)
- [由 Hub 分发更新，包括登录节点后的计算节点](docs/deployment.md#由-hub-自动更新节点)

## Ghostty 与主题

我们强烈建议使用 [Ghostty](https://ghostty.org/) 作为 Fleetty 的终端客户端。仓库提供 [Fleetty Dark](themes/ghostty/Fleetty%20Dark) 和 [Fleetty Light](themes/ghostty/Fleetty%20Light)，分别使用炭黑和暖纸背景，浅色光标为纯黑。

在**客户端电脑**安装主题后，将 Ghostty 的 `theme` 设置替换为：

```ini
theme = dark:Fleetty Dark,light:Fleetty Light
```

Ghostty 跟随系统外观切换，Fleetty 默认的 `auto` 主题可在本地与 SSH 会话中跟随终端，无需转发主题环境变量。按 `t` 后，当前会话改为手动选择。

[主题下载、安装与 Powerlevel10k 配置](docs/themes.md)。为了保持稳定的对比度，建议 Ghostty 使用 `background-opacity = 1`；透明度、字体和快捷键由用户配置决定。

## 界面预览

以下截图来自 Fleetty 的实际运行界面。

按机器类型集中展示在线状态和核心指标，节点离线时保留最后在线时间并自动重试。

<a href="docs/images/fleetty-hub.webp">
  <img src="docs/images/fleetty-hub.webp" alt="Fleetty Machine Hub 总览" width="100%">
</a>

<table>
  <tr>
    <td width="50%" valign="top">
      <strong>GPU 计算节点</strong><br>
      CPU、内存、磁盘、网络、GPU、Slurm 节点队列和进程统一呈现。<br><br>
      <a href="docs/images/fleetty-gpu.webp">
        <img src="docs/images/fleetty-gpu.webp" alt="Fleetty GPU 计算节点监控页面">
      </a>
    </td>
    <td width="50%" valign="top">
      <strong>NAS 与服务节点</strong><br>
      突出网络、挂载点、Docker、PM2 和 HTTP 服务健康状态。<br><br>
      <a href="docs/images/fleetty-nas.webp">
        <img src="docs/images/fleetty-nas.webp" alt="Fleetty NAS 节点监控页面">
      </a>
    </td>
  </tr>
</table>

## 常用操作

| 按键 | 功能 |
| --- | --- |
| `1` / `2` / `3` | Overview / Compute / Network |
| `4` | 本地 Storage Map |
| `Tab` / `Shift+Tab` | 切换页面 |
| `↑` / `↓`、单击 | 选择进程或节点 |
| `Enter` / `Esc` | 详情 / 返回 |
| `/` / `c` | 过滤 / 清除过滤 |
| `o` / `O` | 切换排序字段 / 反转顺序 |
| `l` / `t` / `r` / `q` | 布局 / 主题 / 刷新 / 退出 |

Compute 页在 32 核及以下使用逐核横条，更多核心使用紧凑纵向柱图：柱宽自适应，历史图放在右侧窄栏；按 `f` 聚焦核心、`v` 切换完整明细。

Network 页显示上下行和累计流量；Linux TCP 连接按 `DOWN + UP` 实时速率排序，并显示所属用户。不支持流量计数或权限不足时显示缺失提示，不将未知数据当作零。

Storage Map 仅在本地 `fleetty top` 提供。删除、压缩和去重都有独立确认；远程监控不会获得文件删除权限。

[完整操作与指标说明](docs/usage.md) · [管理模式与权限边界](docs/security.md)

## 更新

```bash
fleetty update --check             # 只检查
fleetty update                     # 最新正式 Release
fleetty update --version v0.1.11    # 指定版本
```

更新会校验安装包并原子替换当前程序，保留旧版备份；不会修改配置、密钥或自动重启服务。服务运行者需要重启对应 unit，客户端重新连接后使用新版。系统安装由有写权限的管理员执行。

多机更新使用 `fleettyctl update`，可由 Hub 缓存一次安装包后分层分发。

[单机安装与升级](docs/installation.md) · [批量部署与定时更新](docs/deployment.md)

## 详细文档

| 主题 | 文档 |
| --- | --- |
| 平台要求、安装、自更新与卸载 | [安装与升级](docs/installation.md) |
| 页面、逐核 CPU、频率、网络与存储地图 | [使用指南](docs/usage.md) |
| 终端主题、自动跟随与 prompt 配色 | [Ghostty 与主题](docs/themes.md) |
| 多服务器与节点认证配置 | [Hub](docs/hub.md) |
| 多集群、作业状态、QOS 与窄屏队列 | [Slurm](docs/slurm.md) |
| Docker、PM2、挂载点与 HTTP 健康检查 | [NAS](docs/nas.md) |
| 幂等安装、fleet manifest 与分层更新 | [部署与运维](docs/deployment.md) |
| 管理密码、公钥和特权助手 | [安全与管理](docs/security.md) |
| 配置项、服务日志与常见问题 | [配置与故障排查](docs/configuration.md) |
| 采集频率、连接复用与历史数据 | [采集与性能](docs/performance.md) |

## 从源码构建

Go 版本以 [go.mod](go.mod) 为准。

```bash
go test -race ./...
go vet ./...
go build -o fleetty ./cmd/fleetty
go build -o fleettyctl ./cmd/fleettyctl
```
