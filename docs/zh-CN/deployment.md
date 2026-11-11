[English](../en/deployment.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# 批量部署与 Hub 自动更新

Fleetty 自带可以重复运行的安装器。`--scope auto` 对普通账户选择 `user`，对 root 选择 `system`。单机：

```bash
./fleetty_linux_amd64 install \
  --role node \
  --scope user \
  --config-dir ./node-config
~/.local/bin/fleetty doctor --role node --scope user
```

Hub 使用 `--role hub`。安装器按该范围原子写入二进制、配置和 systemd 单元。文件没有变化就不会重启服务。服务启动失败时，二进制、单元和本次的配置变更会回滚。

配置文件属主是服务账户，权限 `0600`。安装器拒绝符号链接、子目录、隐藏文件和超过大小限制的文件。暂存目录里没有的已有文件会保留。

多台机器用 `fleettyctl` 和一份 JSON fleet manifest 描述。控制机可以是 Linux 或 macOS。SSH 使用本机 OpenSSH 配置，包括代理和 host key 检查：

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

`fleettyctl status --json` 除了部署状态，还会在每台目标上读一次实时指标快照，并合并进 `metrics` 字段，供脚本和外部监控使用。需要 Prometheus 文本时：

```bash
fleettyctl metrics --file fleet.json
fleettyctl metrics --file fleet.json --target gpu-1
```

`metrics` 复用 manifest 里每台目标的 SSH 通道，在远端执行 `fleetty metrics`。它不额外开端口，也不需要节点 RPC 密钥。文本包含 CPU、内存、磁盘、网络、GPU、挂载点、服务健康以及 Docker/PM2 计数。`--json` 按目标返回可机读结果。在节点本机，`fleetty snapshot`（可选 `--processes`）或 `fleetty metrics --config machine.json` 可以输出同类数据，例如交给 node_exporter 的 textfile collector。快照的 `history` 字段是最近 60 分钟，不是磁盘上的整份 JSONL。见 [采集与性能](performance.md)。

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

`binary` 和 `config_dir` 相对于 manifest 所在目录解析。单个目标可以用自己的 `binary` 覆盖全局值。`role` 为 `node`、`hub` 或 `privileged-helper`。助手只能是系统范围。`scope: "user"` 默认 `become: "none"`，全程不调用 sudo。`scope: "system"` 默认 `become: "sudo"`。root SSH 会话可以显式设置 `"become": "none"`。

manifest 没有密码字段。SSH 凭据由 OpenSSH 管理。Fleetty 的管理密码、RPC 私钥和其他秘密应放在目标的 `config_dir` 中，并且只有执行部署的账户可以读。`plan` 比较二进制和受管配置的 SHA-256，以及服务是否启用、是否在运行。`apply` 只处理有差异的目标。远端安装仍然是同一个原子安装器。没有 `--yes` 时 `apply` 会拒绝执行。

只有系统范围的部署账户等同于 root 管理员。`become: "sudo"` 需要非交互的管理员 sudo，这不是低权限授权。用户范围只管理该 SSH 用户自己的 Fleetty 文件和用户服务，适合作为默认方式。

## 由 Hub 自动更新节点

Hub 可以作为唯一下载并分发 Fleetty 安装包的位置。每一轮只下载一次 Release 资产，校验 SHA-256，写入本地缓存，再按更新清单声明的邻接关系向外走。Hub 更新它能直接到达的节点。登录节点后面的计算节点由该登录节点更新。远端仍然使用 Fleetty 自己的安装器，服务起不来就回滚。关机或暂时不可达的机器标记为 `DEFERRED`。只有它所在的分支暂停，其他节点继续，下一轮会再试这个分支。

在 Hub 上准备部署用的 SSH 密钥和 OpenSSH host key 检查，然后安装 `fleettyctl`：

```bash
sudo install -d -o root -g root -m 0755 /opt/fleetty
sudo install -o root -g root -m 0755 \
  fleettyctl_linux_amd64 /opt/fleetty/fleettyctl
sudo install -o root -g root -m 0600 \
  fleet-update.example.json /etc/fleetty/fleet-update.json
```

更新清单没有 `binary` 字段。它声明要下载的 release：

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

`ssh` 由目标的父节点解释。顶层条目是 Hub 上的 OpenSSH 别名。`children` 里的别名只需要在拥有它的 `relay` 登录节点上可用。Hub 不需要叶子节点的路由、私钥或 `known_hosts`。`relay` 不安装也不重启服务。它的父节点发送经过校验的临时 `fleettyctl`、子树清单，以及这棵子树需要的二进制。relay 对邻居做计划或更新，然后删掉这些文件。私钥和 SSH agent 不会随安装包转发。

`relay` 里面还可以再放 `relay`。深度最多 8，整棵树最多 512 个目标。嵌套目标必须写 `arch`，这样 Hub 不用探测叶子节点也能打好完整的离线包。只有 Hub 直接拨号的顶层目标可以省略 `arch`，这时 Hub 会执行 `uname -m`。每一跳都应使用 `StrictHostKeyChecking` 和自己的部署密钥。系统范围要求相邻部署账户可以执行非交互的 `sudo -n`。用户范围不调用 sudo。监控用的 RPC 密钥不会拿来做软件更新，因此只读监控密钥不能变成系统管理员。

你只在 Hub 上运行 `fleettyctl update`。`cascade` 子命令由相邻的 relay 调用。它要求清单里每个临时二进制都带有匹配的 SHA-256，并且不需要自己的定时器。

先看计划，再执行：

```bash
sudo /opt/fleetty/fleettyctl update \
  --file /etc/fleetty/fleet-update.json
sudo /opt/fleetty/fleettyctl update \
  --file /etc/fleetty/fleet-update.json --yes
```

要按计划自动运行，安装 Release 附带的 systemd 单元和定时器。定时器在开机 15 分钟后触发，之后每 6 小时一次，并带随机延迟，避免许多 Hub 同时访问下载源：

```bash
sudo install -o root -g root -m 0644 \
  fleetty-update.service fleetty-update.timer \
  /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now fleetty-update.timer
systemctl list-timers fleetty-update.timer
```
