# NAS 与服务监控

[返回 README](../README.md)

将节点配置为 `nas` 后，默认页面会改为存储服务器视图，重点展示指定物理网卡的实时吞吐、累计流量、错误与丢包，各挂载点的容量告警，以及 Docker、PM2 和 HTTP 服务的健康状态。Docker 表格包含镜像、健康检查、CPU、内存、网络 I/O、进程数、重启次数、运行时间和端口；PM2 表格包含应用状态、实例 ID、PID、CPU、内存、运行时间、重启次数和执行模式。管理模式仍然可以查看和终止有权限管理的进程、重启监控服务；重启主机取决于安装 scope 和显式授权。

下载 NAS 配置示例并按实际环境修改。下面展示 system scope；user scope 将文件写入 `~/.config/fleetty`，并使用 `systemctl --user restart fleetty.service`：

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

配置示例：

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

服务账户能够读取本机 Docker socket 时，Fleetty 会只读采集容器状态和资源数据，无需为了该功能直接以 root 运行。设置 `pm2_user` 后，监控程序会查找该用户已经运行的 PM2 daemon，并通过 `pm2 jlist` 读取应用状态；rootless 部署通常应使用同一个 PM2 用户。Fleetty 不会启动 PM2 daemon，也不会修改或重启应用。HTTP 检查由节点本机发起，因此可以检查只监听 `127.0.0.1` 的服务。请只配置可信 URL；监控程序不会读取完整响应正文，也不会显示或保存容器及 PM2 应用的环境变量。
