[English](../en/nas.md) | 简体中文

[返回 README](../../README.zh-CN.md)

# NAS 与服务监控

profile 为 `nas` 的节点会把默认页面换成存储服务器视图：你指定的物理网卡上的实时吞吐、累计流量、错误和丢包；每个挂载点的容量告警；以及 Docker、PM2 和 HTTP 检查的健康状态。Docker 表包含镜像、健康检查、CPU、内存、网络 I/O、进程数、重启次数、运行时间和端口。PM2 表包含状态、实例 ID、PID、CPU、内存、运行时间、重启次数和执行模式。管理模式仍然可以查看并停止该账户有权管理的进程，也可以重启监控服务。重启主机取决于安装范围和显式授权。见 [认证与管理](security.md)。

`profile` 接受 `gpu`、`cpu`、`nas` 和 `general`。`storage` 是 `nas` 的别名。省略 profile 时为 `gpu`，所以 NAS 节点必须设置 `"profile": "nas"`。NAS profile 还不能打开自定义布局编辑器。按 `l` 会提示这些布局尚不可用。

下载示例并按机器修改。下面的命令是系统范围。用户安装把文件写到 `~/.config/fleetty`，并用 `systemctl --user restart fleetty.service` 重启：

```bash
fleetty_release_base="https://github.com/Rhythmicc/Fleetty/releases/latest/download"
curl -fLO "${fleetty_release_base}/machine-nas.example.json"

sudo install -o root -g root -m 0600 \
  machine-nas.example.json \
  /etc/fleetty/machine.json
printf '%s\n' \
  'MACHINE_CONFIG_FILE=/etc/fleetty/machine.json' |
  sudo tee /etc/fleetty/machine.env >/dev/null
sudo chmod 0600 /etc/fleetty/machine.env
sudo systemctl restart fleetty.service
```

示例：

```json
{
  "name": "Storage NAS",
  "profile": "nas",
  "network_interfaces": ["eno1"],
  "mounts": ["/", "/mnt/storage-1", "/mnt/storage-2"],
  "docker": true,
  "pm2_user": "service-account",
  "http_checks": [
    {"name": "Web console", "url": "http://127.0.0.1/"},
    {"name": "Metrics", "url": "http://127.0.0.1:3000/"}
  ]
}
```

挂载路径必须是绝对路径。`http_checks` 的每一项都需要名称，以及带主机名的 `http` 或 `https` URL。服务账户能读取本机 Docker socket 时，Fleetty 以只读方式采集容器状态和资源计数。不必为此把 Fleetty 当成 root 来跑。设置 `pm2_user` 后，Fleetty 查找该用户已经在运行的 PM2 daemon，并用 `pm2 jlist` 读取应用状态。rootless 部署应填写同一个 PM2 用户。Fleetty 不会启动 PM2 daemon，也不会修改或重启应用。HTTP 检查在节点本机发起，因此可以访问只监听 `127.0.0.1` 的服务。只配置你信任的 URL。监控程序不读取完整响应正文，也不显示或保存容器和 PM2 应用的环境变量。

系统节点的服务账户被授予 `CAP_SETUID` 和 `CAP_SETGID`，以便在运行该用户的 PM2 客户端之前降到 `pm2_user`。单元上出现这两项能力只是为了这件事。
