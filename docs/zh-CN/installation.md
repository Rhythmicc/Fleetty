[English](../en/installation.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 安装与升级

## 系统要求

- 本地监控支持 macOS 和常见 Linux 发行版。
- SSH 节点、Hub 和特权助手需要带 systemd 的 Linux。
- 支持 `amd64` 和 `arm64`。
- NVIDIA 指标需要驱动可用，并且 `nvidia-smi` 在 `PATH` 中。

普通用户即可安装和运行 Fleetty。macOS 本地模式包含 CPU、内存、磁盘、网络、进程和 Apple Silicon GPU。Linux 包含 NVIDIA GPU。某个设备或采集器不可用时，其余界面仍然可用。Docker socket 以及其他用户的进程详情，遵循操作系统原有的访问权限。

GPU 卡片只显示实际采到的结果。默认的 `gpu` profile 不会占一个空位。在 Linux 上，如果 `nvidia-smi` 失败，并且 PCI 清单里没有 NVIDIA 显示或 3D 控制器，Overview、Compute 和自定义页面会隐藏 GPU 卡片以及 GPU/CUDA 标记，并把空出来的位置让给其他内容。如果确实有 NVIDIA GPU，或者读不到 PCI 清单，失败提示会留在界面上，避免把驱动、工具或权限问题藏起来。

macOS 上不带机器配置文件的 `fleetty top` 使用 `general` profile。没有机器配置文件的 Linux 服务默认是 `gpu`。两种情况下，卡片仍然只在采集成功时出现，并受上面的 Linux 规则约束。可用的 profile 名称是 `gpu`、`cpu`、`nas` 和 `general`。`storage` 等同于 `nas`，`server` 等同于 `general`。

## 下载与本地运行

按 [快速开始](../../README.zh-CN.md#快速开始) 下载并校验本机对应的正式包。它会把 Fleetty 装到 `~/.local/bin/fleetty`，不创建服务。

```bash
"$HOME/.local/bin/fleetty" top
```

把 `~/.local/bin` 加入 `PATH` 后可以直接运行 `fleetty top`。单独执行 `fleetty` 会打开启动菜单，可以开始本地监控，或查看 SSH 服务的配置和密钥。只有执行 `fleetty serve`，或在菜单里选择 SSH Server，监听才会开始。它不会在后台悄悄打开匿名端口。

在 Mac 上，本地监控会显示 Apple Silicon GPU、电池状态，以及按活跃驻留时间加权的 CPU 频率。具体显示取决于硬件和系统接口。没有电池或没有 GPU 的机器会省略对应区域。参见 [CPU 与历史图](usage.md#cpu-与历史图) 和 [主题](themes.md)。

`fleetty top` 还接受 `--config PATH`、`--theme auto|dark|light` 和 `--layout PATH`。

## Linux SSH 节点

先按快速开始安装 Linux 版，再决定哪些客户端公钥可以连接。下面的服务步骤需要 systemd。前台的 `fleetty serve` 不需要。

把客户端公钥复制到服务器。下面的命令假定它在 `./client.pub`。然后以将要拥有该服务的用户身份运行安装器：

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

用户安装把二进制放在 `~/.local/bin/fleetty`，配置放在 `~/.config/fleetty`，并创建 `fleetty.service` 用户单元。服务监听 `0.0.0.0:23234`，只接受 `authorized_keys` 里的公钥。一行一把钥匙，就可以允许多人访问。

如果用户退出登录后服务仍要继续运行，先看 lingering：

```bash
loginctl show-user "$USER" -p Linger
```

输出为 `Linger=no` 时，请管理员为该账户启用 lingering。也可以在前台运行。监控本身不需要 root：

```bash
SSH_HOST_KEY_PATH="$HOME/.config/fleetty/ssh_host_ed25519" \
SSH_AUTHORIZED_KEYS_FILE="$HOME/.config/fleetty/authorized_keys" \
"$HOME/.local/bin/fleetty" serve
```

如果没有设置 `SSH_HOST_KEY_PATH`，前台 `fleetty serve` 会把 host key 写到工作目录下的 `.ssh/fleetty_ed25519`。systemd 单元会显式设置路径，所以通过服务安装时不会走这个回退。

需要系统服务，以及重启主机这类操作时，管理员可以按系统范围安装：

```bash
sudo "$HOME/.local/bin/fleetty" install \
  --role node \
  --scope system \
  --config-dir "${fleetty_config}"
sudo /opt/fleetty/fleetty doctor --role node --scope system
```

系统安装使用 `/opt/fleetty` 和 `/etc/fleetty`。首次启动会在该配置目录创建 SSH host key。升级时不要删掉它。节点单元使用 `ssh_host_ed25519`。Hub 单元使用 `hub_host_ed25519`。

如果启用了防火墙，只向需要访问监控的网络开放 TCP 23234。

## 命令行自更新

`fleetty update` 替换的是刚才执行的那个二进制，不是另一个目录里的副本：

```bash
fleetty update --check             # report only
fleetty update                     # latest stable release
fleetty update --version v0.1.4    # a specific tag
```

更新器从 GitHub Release 下载当前系统与架构的安装包，校验 SHA-256，并执行新二进制的版本检查。通过之后才在同一文件系统上原子替换。失败不会覆盖原文件。重叠的第二次更新会被拒绝。旧二进制保留在命令打印的 `Backup` 路径。符号链接会解析到真实文件。权限保持不变，root 更新时也保留原属主。

用户安装不需要 sudo。系统安装由对该目录有写权限的人执行：

```bash
sudo /opt/fleetty/fleetty update
```

更新不修改配置和密钥，也不重启服务。已经打开的会话仍使用旧二进制。按安装范围重启对应单元：

```bash
systemctl --user restart fleetty.service       # user node
systemctl --user restart fleetty-hub.service   # user Hub
sudo systemctl restart fleetty.service         # system node
sudo systemctl restart fleetty-hub.service     # system Hub
```

向多台机器发布，包括登录节点后面的节点，见 [部署与运维](deployment.md)。

## 重新运行安装器

新二进制还要刷新受管单元，或者需要导入配置时，重新运行安装器。使用已经下载并校验过的二进制：

```bash
"$HOME/.local/bin/fleetty" install --role node --scope user
```

安装器管理的内容没有变化时，服务不会重启。服务启动失败时，本次写入的二进制、单元和配置会回滚。没有提供的文件不会导致已有公钥、RPC 密钥、管理文件或 SSH host key 被删除。Hub 使用 `--role hub`。系统服务加上 `sudo` 和 `--scope system`。

## 卸载

```bash
systemctl --user disable --now fleetty.service
rm "$HOME/.config/systemd/user/fleetty.service"
systemctl --user daemon-reload
rm "$HOME/.local/bin/fleetty"
```

如果还要删除管理配置和 SSH host key，再删除 `~/.config/fleetty`。系统范围使用 `sudo systemctl`，路径是 `/etc/systemd/system/fleetty.service`、`/opt/fleetty` 和 `/etc/fleetty`。

Hub 的单元名是 `fleetty-hub.service`。用户范围仍然用 `systemctl --user`，系统范围用 `sudo systemctl`。`nodes.json` 里是节点地址和 host key 指纹，保留或删除都可以。
