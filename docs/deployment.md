# 批量部署与 Hub 自动更新

[返回 README](../README.md)

Fleetty 自带幂等安装器。`--scope auto` 会在普通用户下选择 `user`，在 root 下选择 `system`。单机更新示例：

```bash
./fleetty_linux_amd64 install \
  --role node \
  --scope user \
  --config-dir ./node-config
~/.local/bin/fleetty doctor --role node --scope user
```

Hub 使用 `--role hub`。安装器会原子写入对应 scope 的二进制、配置和 systemd unit；文件内容没有变化时不会重启服务。如果服务启动失败，二进制、unit 和本次配置变更会自动回滚。

配置文件会由服务账户所有并使用 `0600` 权限。安装器拒绝符号链接、子目录、隐藏文件和超过限制的文件，未出现在配置目录中的现有配置不会被删除。

多机环境使用 `fleettyctl` 和 JSON fleet manifest。控制端可以是 Linux 或 macOS，SSH 连接沿用本机 OpenSSH config、代理和 host key 校验：

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
fleettyctl_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64) fleettyctl_arch=amd64 ;;
  arm64|aarch64) fleettyctl_arch=arm64 ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
fleettyctl_asset="fleettyctl_${fleettyctl_os}_${fleettyctl_arch}"
curl -fL \
  -o "./${fleettyctl_asset}" \
  "${fleetty_release_base}/${fleettyctl_asset}"
curl -fL \
  -o ./fleetty-checksums.txt \
  "${fleetty_release_base}/checksums.txt"
fleettyctl_expected="$(
  awk -v asset="${fleettyctl_asset}" '$2 == asset { print $1 }' \
    ./fleetty-checksums.txt
)"
if [ "${fleettyctl_os}" = "darwin" ]; then
  fleettyctl_actual="$(shasum -a 256 "./${fleettyctl_asset}" | awk '{ print $1 }')"
else
  fleettyctl_actual="$(sha256sum "./${fleettyctl_asset}" | awk '{ print $1 }')"
fi
test -n "${fleettyctl_expected}"
test "${fleettyctl_expected}" = "${fleettyctl_actual}"
mv "./${fleettyctl_asset}" ./fleettyctl
chmod 0755 ./fleettyctl

fleettyctl validate --file fleet.json
fleettyctl plan --file fleet.json
fleettyctl apply --file fleet.json --yes
fleettyctl status --file fleet.json
```

`fleettyctl status --json` 除部署状态外，还会在每台目标上读取一次实时指标快照并合并到 `metrics` 字段，便于脚本和外部监控系统消费。需要完整 Prometheus 文本时使用：

```bash
fleettyctl metrics --file fleet.json
fleettyctl metrics --file fleet.json --target gpu-1
```

`metrics` 复用 manifest 中每台目标的 SSH 通道在远端执行 `fleetty metrics`，不开放额外端口，也不需要节点 RPC 密钥。输出包含 CPU、内存、磁盘、网络、GPU、挂载点、服务健康以及 Docker/PM2 计数，`--json` 模式返回按目标划分的可机读结果。节点本机也可以直接调用 `fleetty snapshot`（可选 `--processes`）或 `fleetty metrics --config machine.json`，例如配合 node_exporter 的 textfile collector 使用。

示例：

```json
{
  "version": 1,
  "binary": "./fleetty_linux_amd64",
  "parallel": 4,
  "timeout_seconds": 15,
  "targets": [
    {
      "name": "gpu-1",
      "ssh": "gpu-1-admin",
      "role": "node",
      "scope": "user",
      "config_dir": "./config/gpu-1"
    },
    {
      "name": "lab-hub",
      "ssh": "hub-admin",
      "role": "hub",
      "scope": "system",
      "config_dir": "./config/hub"
    }
  ]
}
```

`binary` 和 `config_dir` 相对于 manifest 所在目录解析；单个目标可以用自己的 `binary` 覆盖全局值。`role` 可以为 `node`、`hub` 或仅支持 system scope 的 `privileged-helper`。`scope: "user"` 默认 `become: "none"`，整个部署过程不调用 sudo；`scope: "system"` 默认使用 `become: "sudo"`，使用 root SSH 时可以显式设置 `"become": "none"`。

manifest 不接受密码字段。SSH 凭据由 OpenSSH 管理，Fleetty 管理密码、RPC 私钥等敏感内容应放在目标的 `config_dir` 中，并确保该本地目录只有部署账户可读。`plan` 会比较二进制与受管配置的 SHA-256、服务启用状态和运行状态；`apply` 只处理有差异的目标，远端安装仍由同一个原子安装器完成。

只有 system scope 的部署账户等同于 root 级管理员：`become: "sudo"` 要求非交互式管理员 sudo，不能把它视为低权限授权。user scope 只管理该 SSH 用户自己的 Fleetty 文件和 user service，适合作为默认部署方式。

## 由 Hub 自动更新节点

Hub 可以作为 Fleetty 安装包的统一下载与分发点。每轮更新只在 Hub 上下载一次 Release 资产；通过 SHA-256 校验后写入本地缓存，再沿更新清单声明的邻接关系向外传播。直接相连的节点由 Hub 更新，隐藏在登录节点后的计算节点则由该登录节点继续推送。远端仍由 Fleetty 自带安装器原子替换，服务无法正常启动时会自动回滚。关机或暂时不可达的节点会标记为 `DEFERRED`，只暂停它所在的分支，不会阻塞其他节点，并会在下一轮重新尝试。

在 Hub 上准备专用的部署 SSH 密钥和 OpenSSH host key 配置，然后安装 `fleettyctl`：

```bash
sudo install -d -o root -g root -m 0755 /opt/fleetty
sudo install -o root -g root -m 0755 \
  fleettyctl_linux_amd64 /opt/fleetty/fleettyctl
sudo install -o root -g root -m 0600 \
  fleet-update.example.json /etc/fleetty/fleet-update.json
```

更新清单不需要 `binary` 字段，而是声明 release 下载源：

```json
{
  "version": 1,
  "release": {
    "base_url": "https://github.com/Rhythmicc/Fleetty/releases/latest/download",
    "cache_dir": "/var/cache/fleetty/releases"
  },
  "parallel": 3,
  "timeout_seconds": 15,
  "targets": [
    {
      "name": "gpu-login",
      "ssh": "gpu-login",
      "role": "relay",
      "arch": "amd64",
      "children": [
        {
          "name": "gpu-1",
          "ssh": "gpu-1-admin",
          "role": "node",
          "scope": "system",
          "become": "sudo",
          "arch": "amd64"
        }
      ]
    },
    {
      "name": "storage-1",
      "ssh": "storage-1-admin",
      "role": "node",
      "scope": "user",
      "arch": "amd64"
    }
  ]
}
```

`ssh` 始终由目标的父节点解释。顶层条目使用 Hub 上的 OpenSSH 别名；`children` 中的别名只需在它所属的 `relay` 登录节点上可用。因此 Hub 无需获得叶子节点的路由、私钥或 `known_hosts`。`relay` 不会安装或重启服务：父节点向它发送经过校验的临时 `fleettyctl`、子树清单和所需二进制，由它完成相邻节点的计划或更新后立即清理。私钥和 SSH agent 不会随安装包转发。

`relay` 可以继续包含下一层 `relay`，最大深度为 8、整棵树最多 512 个目标。嵌套目标必须显式填写 `arch`，使 Hub 能在无法直接探测叶子节点时准备完整的离线包；只有顶层直连目标可以省略，由 Hub 使用 `uname -m` 探测。每一跳都应启用 `StrictHostKeyChecking` 并使用独立部署密钥。system scope 要求相邻节点的部署账户可以执行非交互式 `sudo -n`，user scope 不调用 sudo。监控 RPC 密钥不会被用于软件更新，避免把只读监控权限扩大成系统管理权限。

管理员始终只需在 Hub 执行 `fleettyctl update`。内部的 `cascade` 子命令由相邻中继自动调用，并要求清单中的每个临时二进制都带有匹配的 SHA-256；它不需要单独配置定时器。

先查看更新计划，再执行一次更新：

```bash
sudo /opt/fleetty/fleettyctl update \
  --file /etc/fleetty/fleet-update.json
sudo /opt/fleetty/fleettyctl update \
  --file /etc/fleetty/fleet-update.json --yes
```

需要自动运行时，安装随 Release 提供的 systemd unit。默认在开机 15 分钟后执行，之后每 6 小时检查一次，并加入随机延迟避免多套 Hub 同时访问下载源：

```bash
sudo install -o root -g root -m 0644 \
  fleetty-update.service fleetty-update.timer \
  /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now fleetty-update.timer
systemctl list-timers fleetty-update.timer
```
