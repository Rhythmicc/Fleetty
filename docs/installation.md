# 安装与升级

[返回 README](../README.md)

## 系统要求

- 本地监控支持 macOS，以及常见 Linux 发行版；
- SSH 节点、Hub 和特权助手需要使用 systemd 的 Linux；
- 支持 `amd64` 和 `arm64` 架构；
- NVIDIA GPU 指标需要系统已安装驱动并能执行 `nvidia-smi`。

普通用户即可安装和运行 Fleetty。macOS 本地模式支持 CPU、内存、磁盘、网络、进程和 Apple Silicon GPU；Linux 支持 NVIDIA GPU。硬件或采集接口不可用时，其余指标仍可使用。Docker socket、其他用户的进程详情等指标始终遵循操作系统原有访问权限。

GPU 卡片按实际采集结果显示，不由默认的 `gpu` profile 强制占位。Linux 的 NVIDIA 采集失败时，如果 PCI 硬件清单确认没有 NVIDIA GPU，Overview、Compute 和自定义页面会隐藏 GPU 卡片及 GPU/CUDA 标记，并重新利用空出的布局空间；存在 NVIDIA GPU，或无法读取硬件清单时，仍显示采集失败提示，避免掩盖驱动、工具或权限问题。

## 下载与本地运行

按 [README 的快速开始](../README.md#快速开始)下载并校验当前平台的正式安装包。它会将 Fleetty 安装到 `~/.local/bin/fleetty`，不创建后台服务。

```bash
"$HOME/.local/bin/fleetty" top
```

可以将 `~/.local/bin` 加入 `PATH` 后直接使用 `fleetty top`。裸命令 `fleetty` 打开启动菜单，可选择本地监控或检查 SSH 服务的配置与认证状态。只有明确执行 `fleetty serve`，或选择 SSH Server 后，才会启动监听；不会静默开启匿名访问。

macOS 本地监控显示 Apple Silicon GPU、系统电池状态以及 CPU 活跃驻留时间加权频率；指标依硬件和系统接口而定。没有电池或 GPU 的设备会省略相应区域。参见[指标说明](usage.md#cpu-与历史图)和[自动主题](themes.md)。

## Linux SSH 节点

先按快速开始安装 Linux 版，再配置允许连接监控服务的公钥。以下服务安装步骤需要 systemd；前台 `fleetty serve` 不依赖 systemd。

先将客户端公钥复制到服务器，以下假设保存为 `./client.pub`。然后以当前普通用户运行内置安装器：

```bash
fleetty_config="$(mktemp -d)"
chmod 0700 "${fleetty_config}"
install -m 0600 ./client.pub \
  "${fleetty_config}/authorized_keys"

"$HOME/.local/bin/fleetty" install \
  --role node \
  --scope user \
  --config-dir "${fleetty_config}"
"$HOME/.local/bin/fleetty" doctor --role node --scope user
```

用户安装将程序放在 `~/.local/bin/fleetty`，配置放在 `~/.config/fleetty`，并创建 `fleetty.service` systemd user unit。服务默认监听 `0.0.0.0:23234`，但只接受 `authorized_keys` 中登记的客户端公钥。如需允许多人访问，将多行公钥写入同一个文件。

如果服务需要在用户完全退出后继续运行，请检查：

```bash
loginctl show-user "$USER" -p Linger
```

`Linger=no` 时可请管理员为该服务账户启用 lingering。无法使用 systemd user service 时，也可以以前台方式运行，监控功能本身不依赖 root：

```bash
SSH_HOST_KEY_PATH="$HOME/.config/fleetty/ssh_host_ed25519" \
SSH_AUTHORIZED_KEYS_FILE="$HOME/.config/fleetty/authorized_keys" \
"$HOME/.local/bin/fleetty" serve
```

需要统一的系统级服务以及重启主机等特权能力时，管理员可以改用：

```bash
sudo "$HOME/.local/bin/fleetty" install \
  --role node \
  --scope system \
  --config-dir "${fleetty_config}"
sudo /opt/fleetty/fleetty doctor --role node --scope system
```

系统安装使用 `/opt/fleetty` 和 `/etc/fleetty`。两种安装首次启动时都会在各自配置目录创建 SSH host key，请勿在升级时删除。

如果服务器启用了防火墙，请只向需要访问监控的网络开放 TCP 23234 端口。

## 命令行自更新

更新的是当前执行的 Fleetty，而不是另一个安装目录中的副本：

```bash
fleetty update --check             # 只检查
fleetty update                     # 最新正式 Release
fleetty update --version v0.1.11    # 指定版本
```

更新器从 GitHub Release 下载当前系统与架构的安装包，校验 SHA-256，并执行新版的版本检查；通过后才在同一文件系统内原子替换。失败不会覆盖原程序，并发更新会被拒绝，旧版保留在输出的 `Backup` 路径。符号链接解析到实际文件，文件权限和 root 更新时的原属主保持不变。

用户安装不需要 sudo。系统安装由对目录有写权限的管理员更新，例如：

```bash
sudo /opt/fleetty/fleetty update
```

更新不会修改配置或密钥，也不会自动重启服务；正在运行的会话仍使用旧版。按安装 scope 和角色重启：

```bash
systemctl --user restart fleetty.service       # user 节点
sudo systemctl restart fleetty.service         # system 节点
sudo systemctl restart fleetty-hub.service     # system Hub
```

多节点发布与登录节点后的分层更新见[部署与运维](deployment.md)。

## 重新运行安装器

需要同时更新受管 service unit 或导入配置时，可使用已下载、校验的新二进制重新运行安装器：

```bash
"$HOME/.local/bin/fleetty" install --role node --scope user
```

安装器对未变化的内容不做重启；服务启动失败时会回滚本次写入的二进制、unit 和配置。未提供的新配置不会导致现有公钥、RPC 密钥、管理配置或 SSH host key 被删除。Hub 使用 `--role hub`；系统服务在命令前加 `sudo` 并指定 `--scope system`。

## 卸载

```bash
systemctl --user disable --now fleetty.service
rm "$HOME/.config/systemd/user/fleetty.service"
systemctl --user daemon-reload
rm "$HOME/.local/bin/fleetty"
```

如需同时删除管理配置和 SSH host key，再删除 `~/.config/fleetty`。system scope 的卸载命令使用 `sudo systemctl`，对应路径为 `/etc/systemd/system/fleetty.service`、`/opt/fleetty` 和 `/etc/fleetty`。

安装了 Hub 时，将服务名换成 `fleetty-hub.service`；user scope 继续使用 `systemctl --user`，system scope 使用 `sudo systemctl`。`nodes.json` 中只包含节点地址和 host key 指纹，可以按需要保留或删除。
