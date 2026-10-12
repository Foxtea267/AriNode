# V2bX 一键迁移与恢复

适用于 Linux amd64/arm64、systemd 和原版 V2bX 官方脚本安装路径。迁移会短暂切换监听服务，目标健康后才卸载源运行组件。

## 安装 AriNode，同时迁移 V2bX

```sh
curl -fsSL https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh | sudo bash -s -- --migrate-v2bx
```

先选择中文或英文，部署方式由参数选择，无须重新输入面板地址、密钥或节点 ID。普通安装菜单也可选择第 4 项。无交互终端时默认英文，可用 `sudo env ARINODE_LANGUAGE=zh bash -s -- --migrate-v2bx` 指定中文。

已安装 AriNode：

```sh
sudo anctl migrate from-v2bx
anctl status
anctl log
```

默认执行完整迁移，不需要额外的 `--switch` 或 `--uninstall`。

## 保留、切换和卸载

- 读取 `/etc/V2bX/config.json`，兼容注释、平铺或 `ApiConfig` / `Options` 嵌套配置，展开本地 Include。保留 Core、节点、限速、监听/出口 IP、证书、DNS、自定义路由及出口。远程 Include 需要先下载为已审阅的本地文件。
- 将原配置目录、程序目录、管理入口、systemd unit/drop-ins 保存到 `/var/lib/arinode/migrations/v2bx/snapshot-*`，仅 root 可访问。原二进制附 SHA256，恢复前校验；不依赖重新下载 V2bX。
- 将证书、原生 JSON 和资源复制到 `/etc/arinode/v2bx-assets/`，重写原目录路径，包括原生 JSON 内引用。外部绝对路径继续使用原位置。ApiKey、密码和私钥不改写。相对配置文件路径按原程序工作目录解析，本地 Include 按配置目录展开。
- 备份已有 AriNode 配置，检查新配置，再停止 V2bX、启动 AriNode。本地状态 API 必须显示迁移节点全部 `running`。失败时保留原安装、恢复旧配置并尝试重新启动 V2bX。
- AriNode 健康后删除 `/etc/V2bX`、`/usr/local/V2bX`、V2bX unit/drop-ins、`/usr/bin/V2bX` 与 `/usr/bin/v2bx`。保留迁移快照；其他服务、Komari Agent 和外部证书目录不在卸载范围。

路径大小写敏感。Docker、OpenRC、非标准路径请使用 `--config-only`，自行切换部署。目录 symlink 会被拒绝；证书文件 symlink 会复制其内容。迁移快照和导入资源会占用磁盘空间，不会自动删除。

## 一键迁回 V2bX

```sh
sudo anctl migrate to-v2bx
v2bx status
```

恢复保存的原版本程序和 unit，导出 **当前** AriNode 节点配置，将引用资源复制到 `/etc/V2bX/arinode-import/`。V2bX 连续通过存活检查后，备份 AriNode 配置和程序，卸载 AriNode 运行组件并关闭自动升级 timer。迁移快照继续保留，不要删除 `/var/lib/arinode/migrations/v2bx`。

要求快照完整、CPU 架构相同、目标路径未被其他 V2bX 安装占用。没有快照时，先安装 V2bX，再用 `--config-only` 导出。原版 V2bX 没有 AriNode 的状态 API，迁回的服务存活检查不能代替客户端连接验收。

原版 V2bX 不支持 AriNode 的机器认证、AnyTLS/Mieru 节点和 Cluster 上报。无法兼容的节点会在修改服务前报错；Komari 配置会提醒未导出。共享节点迁回时必须明确放弃组聚合：

原版 V2bX 也不会应用 AriNode 的面板 custom routing/multiplex。本地原生 Core 配置文件仍保留，切换前请确认需要的出口路由已在本地配置中实现。

```sh
sudo anctl migrate to-v2bx --drop-cluster
```

这会恢复原版报告行为，失去跨副本在线报告合并和 IP 身份识别，不会更改面板套餐设备限制。

## 四台服务器共享一个节点

AriNode 插件支持最多 **128 个活跃成员**，没有 3 台服务器上限。先更新 XBoard 插件到 **0.3.0**，再把四台统一迁移为 AriNode 节点组。

第一台执行：

```sh
curl -fsSL https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh | \
  sudo bash -s -- --migrate-v2bx --cluster-domain pool.example.com --cluster-member hk-01
```

其他三台分别使用 `hk-02`、`hk-03`、`hk-04`。域名替换成面板节点地址。参数为全部迁移节点启用同一组域名；配置含不同域名时先普通迁移，再分别配置 Cluster。已安装 AriNode、仍有 V2bX 时可以执行：

```sh
sudo anctl migrate from-v2bx --cluster-domain pool.example.com --cluster-member hk-04
```

插件按 MemberID 保存快照，对每个用户 IP 求并集，并归一化 IPv4-mapped IPv6 和等价 IPv6 表示。同一客户端换到第四台，仍是同一个设备。普通 AriNode 报告模式也会尝试插件的受服务器密钥保护的 `/api/v1/arinode/devices/alivelist`，获取已有 IP 身份；旧面板保留原生数量限制。

设备上限为 3 时，已在线客户端可在四个成员间切换，新的第四个客户端 IP 仍被拒绝。本机在面板同步前也会原子检查新 IP。跨服务器上报存在异步窗口，未实现分布式准入锁。Debug 限额日志仅包含 node、device_limit、observed_devices。

“第四台服务正常、用户连不上”也可能涉及 TLS/Reality、UDP 防火墙、端口、DNS 或客户端选择。分别用四台 IP 作为客户端连接地址，保持面板 SNI、密码、协议参数一致，逐台测试；提供失败机器日志和测试用户 device_limit 才能确认实际原因。完整部署和成员查询见 [节点组说明](node-cluster.md)。

## 预览或仅转换

```sh
anctl migrate from-v2bx --dry-run
anctl migrate to-v2bx --dry-run --drop-cluster
anctl migrate from-v2bx --config-only --input /path/config.json --output /path/arinode.json
anctl migrate to-v2bx --config-only --input /path/arinode.json --output /path/v2bx.json
```

预览不输出密钥、不写文件、不停止服务。配置转换不复制资源或卸载服务，需保留原资源路径。默认覆盖前会备份；`--force=false` 拒绝覆盖。自定义本地状态地址用 `--status-url http://127.0.0.1:端口/v1/status`，认证时设置 `ARINODE_STATUS_TOKEN`。

测试覆盖配置/Include/证书保留、原生 JSON 路径、文件卸载恢复、当前配置往返、失败回滚、快照篡改拒绝、四成员聚合、并发设备限额及 Xray/sing-box 四接收端真实认证转发。服务切换测试使用隔离目录及 systemctl 边界替身，尚未在你的四台机器上执行迁移或验收。

本次完整协议回归还修复了 VLESS HTTPUpgrade 升级握手丢失早到协议数据导致的偶发 EOF，并补齐 Trojan HTTPUpgrade 入口。握手前端保留 HTTP 读取缓冲，认证、用户同步、mux、统计和路由继续使用原生协议服务。
