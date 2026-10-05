# 多服务器共用一个 Xboard 节点

同一个 Xboard 节点可以在多台 AriNode 服务器上运行。订阅只显示一个节点，节点地址填写域名；为域名添加多个 A/AAAA 记录，DNS 和客户端选择其中一台服务器建立连接。节点组适用于 AriNode 支持的全部协议，配置和用户仍通过 Xboard 原生接口读取。

## 部署顺序

1. **先更新 Xboard 插件至 0.2.0 或更新版本**：上传并启用 [`arinode-xboard-plugin.zip`](../dist/arinode-xboard-plugin.zip)。面板需要当前 Xboard 的 `server.v2` 中间件、DeviceStateService 和可用的共享 Redis；运行多个面板实例时须连接同一个 Redis。插件只生成配置和聚合数据，不创建节点。
2. 在 Xboard 创建一个节点，例如 ID `7`，地址填写 `pool.example.com`，配置协议、端口和 TLS/Reality 参数。所有后端使用同一个节点 ID、面板地址和可读取该节点的密钥。机器认证时使用节点绑定的**同一个逻辑机器 ID 和 token**；原生 Xboard 一个节点只能绑定一台逻辑机器，不能给每个后端填写不同的机器 ID。
3. 在每台服务器升级 AriNode：`sudo anctl upgrade`。备份 `/etc/arinode/config.json`，参考[节点组配置](../example/node-cluster.config.json)，在这个节点条目内增加：

   ```json
   "Cluster": {
     "Domain": "pool.example.com",
     "MemberID": "hk-01"
   }
   ```

   第二台填 `hk-02`，后续成员依次填写独立 ID。同组不能重复 ID，否则后一个报告会替换前一个。`Domain` 必须与 Xboard 节点地址一致。组身份由面板和节点 ID 决定，域名只是地址校验；同名域名的不同节点仍分别统计。没有 `Cluster` 的节点保持原有单机上报方式。
4. 在所有后端配置相同的协议、监听端口、传输路径、SNI、Reality 密钥和服务端密钥；本机防火墙放行所用 TCP/UDP 端口。节点组会忽略机器自动发现，只启动本机明确填写的组节点，避免顺带部署其他机器绑定。
5. 执行 `sudo anctl restart`，再用 `anctl log` 和本机 `curl http://127.0.0.1:18086/v1/status` 检查。状态会包含 `cluster_domain`、`cluster_member`；插件缺失、域名不匹配或 Redis 不可达会报错，并独立重试该节点。
6. 添加多条 DNS 记录，再刷新订阅。下面是文档示例地址，部署时替换成真实 IP：

   | 类型 | 名称 | 地址 | TTL |
   | --- | --- | --- | --- |
   | A | pool.example.com | 192.0.2.10 | 60 秒或服务商允许的最小值 |
   | A | pool.example.com | 192.0.2.11 | 同上 |
   | AAAA | pool.example.com | 2001:db8::10 | 可选，须有可用 IPv6 服务 |

   普通代理协议使用 **DNS only/仅 DNS**。普通 HTTP CDN 的代理开关不能代替 TCP/UDP 节点转发。已有 TCP/QUIC 会话继续留在原来的后端。

## 命令生成配置

已安装 AriNode 的新服务器可执行下列命令；第二台只改变 `--cluster-member`。命令拒绝覆盖已有文件，已有部署建议手动增加 `Cluster`，保留原来的证书、路由和 Komari 配置。

```sh
read -rsp 'Xboard token: ' ARINODE_PANEL_TOKEN; echo
sudo env ARINODE_PANEL_TOKEN="$ARINODE_PANEL_TOKEN" anctl init \
  --panel https://panel.example.com --node vless:7 \
  --cluster-domain pool.example.com --cluster-member hk-01 \
  --output /etc/arinode/config.json
unset ARINODE_PANEL_TOKEN
sudo anctl restart
```

使用机器 token 时加 `--machine-id 22`。需要替换配置时自行备份后加 `--force`，该选项不会保留原来的自定义字段。

新服务器一键安装并配置节点组：

```sh
read -rsp 'Xboard token: ' ARINODE_PANEL_TOKEN; echo
curl -fsSL https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh | \
  sudo env ARINODE_INSTALL_MODE=fresh ARINODE_LANGUAGE=zh \
    ARINODE_PANEL_URL=https://panel.example.com ARINODE_PANEL_TOKEN="$ARINODE_PANEL_TOKEN" \
    ARINODE_NODES=vless:7 ARINODE_MACHINE_ID=0 \
    ARINODE_CLUSTER_DOMAIN=pool.example.com ARINODE_CLUSTER_MEMBER=hk-01 bash
unset ARINODE_PANEL_TOKEN
```

机器认证替换 token 并设置 `ARINODE_MACHINE_ID=22`。此命令明确选择从零配置；已有 xbnode 先使用原来的迁移命令保留配置，再增加 `Cluster`。AriNode 到 xbnode 的导出会拒绝组配置，因为 xbnode 不支持成员聚合。

## 插件批量下发

管理员登录 Xboard 后，对 `POST /api/v1/arinode/provision` 提交：

```json
{
  "node_ids": [7],
  "core": "sing",
  "cluster_domain": "pool.example.com",
  "cluster_members": ["hk-01", "hk-02"]
}
```

响应的 `replicas[]` 含 `member_id` 和完整 `config`，分别保存到对应服务器，权限设为 `0600`。使用机器认证可加 `"machine_id":22`。所有所选节点的地址须与 `cluster_domain` 一致。配置下发不复制证书文件。

管理员可调用 `GET /api/v1/arinode/cluster/members?node_id=7` 查看成员 ID、最后上报时间、在线用户数和主机资源。接口需要管理员认证，不返回用户 IP 或节点 token。节点接口 `/cluster/info`、`/cluster/report`、`/cluster/alivelist` 使用 Xboard 原生服务器密钥认证，并校验节点权限；不能公开调用。

## 统计与运行边界

- 各成员流量走 Xboard 原生累计计费；在线设备按用户 IP 合并，同一个 IP 切换到另一后端不会再占一个设备名额。面板节点在线用户数取成员并集；CPU 为有状态成员的平均值，内存、交换区、磁盘为总和。每台物理服务器的监控继续使用独立 Komari 客户端，不使用共享机器状态覆盖彼此。
- 健康成员继续运行和上报；成员在线快照超过 `max(180 秒, 面板 server_push_interval × 3)` 后，在下一次报告或在线列表查询时清理。单机 IP 活跃记录也会过期；在线数属于周期性的活跃统计。
- 失败报告在进程内保留原始快照和请求 ID，恢复后重试。插件保留 24 小时回执，抑制通常的网络重试重复计费；进程重启会丢失未确认的内存报告，Redis 回执和原生队列不属于同一事务，不能保证崩溃窗口内严格恰好一次计费。
- 设备限制依据异步合并结果，多个后端同时接入时存在同步窗口；用户速度限制仍分别作用于每台后端，不是全组共享的精确限速。
- 所有共享同一节点的后端必须启用组模式；混用原生 xbnode、旧 AriNode 或不带 `Cluster` 的实例，会重新覆盖组在线记录。按上述顺序先安装插件，再统一更新后端，最后公开 DNS 地址。
- **DNS 多 IP 分流不会按 CPU 或带宽调度，不保证均匀分配**。缓存和客户端行为会影响选择，客户端未必在一个 IP 故障后尝试另一个。AriNode 当前不自动修改 DNS；故障后从 DNS 删除对应 IP，生效仍受缓存 TTL 影响。需要健康检查自动摘除时，应另接支持相应协议的负载均衡/DNS 服务。参见 [Cloudflare 对轮询 DNS 的说明](https://www.cloudflare.com/learning/dns/glossary/round-robin-dns/)。
- 使用验证 TLS 时，各后端应部署有效的相同域名证书及对应私钥，或者分别部署对该域名有效的证书。客户端有证书固定校验时需共享证书。多 IP 下 HTTP-01 验证可能请求另一台后端，建议使用共享文件证书或 DNS-01；需要 Reality 时务必在面板显式配置相同密钥。组功能不自动签发或同步证书。

## 验证范围

Go 回归覆盖原生读取与组接口认证参数、插件缺失、报告确认、失败后原快照重试、到期用户 IP 清理、设备切换及机器发现隔离。PHP 回归直接运行插件控制器，在确定性的框架边界替身上验证成员合并、重复报告、空快照、过期清理、其他节点数据保留、状态聚合、管理员/服务器路由绑定和批量配置；Laravel 验证器、原生中间件、真实 Redis 锁及队列投递需在具体面板环境验证。这些测试不构成生产 DNS 或多服务器连接分配实测。
