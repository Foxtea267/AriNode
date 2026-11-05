# DSOM：Dynamic SOCKS5 Outbound Manager

## 能做什么，不能证明什么

DSOM 是独立的 Go 模块，复用 AriNode 已有的 normalized policy、Xray/sing-box SOCKS 出站、路由命名空间和配置同步。它探测实际出口，验证显式配置的 SOCKS5 地址，并在网关、备用网关与已验证的直连代理之间选择。

**出口查询成功不代表能绕开供应商网关。** 必须区分：

| 概念 | 本例 / 可确认的信息 |
| --- | --- |
| SOCKS5 网关 | 显式配置的 `kochab.srvgdx.com:10374`；DNS 答案展示在 gateway.ips |
| 公网出口 IP | 经 SOCKS5 请求 IP 查询源得到的地址；`116.206.253.134` 只是历史观测 |
| 住宅设备 IP | 可能位于 NAT、反向隧道或私有转发网络后，无法仅凭公网出口确认 |
| 可访问的代理地址 | 供应商明确授权并提供的 host:port，且完成 SOCKS5 认证与转发验证 |

没有直连地址时返回 `DIRECT_UNAVAILABLE`，Auto 继续使用网关。不把出口 IP 填回 server，不推测端口，不扫描住宅地址。即使显式代理验证成功，也只证明该地址能代理转发；国家、住宅属性、供应商内部是否继续经过荷兰，不能由一次 IP 查询保证。地理信息只作为附带信息，不决定切换。

## 配置位置

当前检查到的 XBoard `ServerService::buildNodeConfig` 不会任意透传 `protocol_settings.dynamic_outbounds`，AriNode 插件也没有假设存在该字段。因此本版使用 AriNode 启动配置顶层 **`DynamicOutbounds`**；不修改 XBoard 源码或数据库，不要求把 SOCKS 凭据存到面板。

完整文件：[dsom.config.json](../example/dsom.config.json)。示例面板地址、机器 ID、节点 ID、凭据路径均需按实际修改；不要用它覆盖已有多面板/Komari 配置，只合并新增字段。默认内核为 sing-box，Xray 同样支持。独立 Hysteria2 core 不支持此路由扩展；HY2/TUIC 等协议通过 sing-box 可以使用。

```json
{
  "DynamicOutbounds": [{
    "tag": "bd-residential",
    "enabled": true,
    "mode": "auto",
    "gateway": {
      "server": "kochab.srvgdx.com",
      "server_port": 10374,
      "credentials_file": "/etc/arinode/dsom/gateway-auth.json"
    },
    "direct": {"enabled": true},
    "health_check": {
      "interval": 60, "timeout": 5, "retries": 1,
      "failure_threshold": 3, "success_threshold": 2,
      "cooldown": 300, "min_consensus": 2
    },
    "metadata": {"url": "https://ipwho.is/{ip}"}
  }]
}
```

这个示例没有直连地址，所以只会使用现有网关。`enabled=false` 的 tag 保留为拒绝转发的本地出站，避免已有路由意外落回香港 direct。

Endpoint 固定为 SOCKS5；可省略 `type` / `version`，若填写则必须分别为 `socks` / `5`。

### 凭据

凭据文件使用严格 JSON，目录权限 700、文件 600，并归运行 AriNode 的用户所有：

```json
{"username":"YOUR_PROVIDER_USERNAME","password":"YOUR_PROVIDER_PASSWORD"}
```

也可使用 `username_env` / `password_env`，两者必须同时指定；不能与 `credentials_file` 混用。支持无认证 SOCKS5，显式认证字段则不允许为空或降级到无认证。认证仅出现在 SOCKS5 握手，不放入查询服务的 HTTP Authorization。日志和状态不输出上游用户名、密码或本地中继随机密码。

凭据文件每个检测周期重新读取，变化后重新验证并保留上一份可用端点作为回滚。环境变量来自进程启动环境，改变 systemd EnvironmentFile 后需要重新启动进程才能更新环境；需要运行时轮换应使用 credentials_file。

### 直连地址

只有供应商明确提供且授权使用代理地址后，才配置：

```json
"direct": {
  "enabled": true,
  "server": "authorized-bd-proxy.example.com",
  "server_port": 1080,
  "credentials_file": "/etc/arinode/dsom/direct-auth.json"
}
```

或者设置 `"direct":{"enabled":true,"endpoint_file":"/etc/arinode/dsom/direct.json"}`。文件格式见 [dsom-direct.example.json](../example/dsom-direct.example.json)，权限同样为 600。文件内容变化在下次检测自动读取，无需重启服务。不能同时填写 endpoint_file 和 server/server_port。新地址完成多源转发验证和成功阈值后才启用；验证失败时继续使用尚可用的旧直连，或在旧直连失败后回退网关。

网关本身及解析到相同 gateway IP:port 的别名不算直连绕过，会被拒绝提升为 Direct。不会根据 WHOIS/出口 IP 自动构造新代理地址。

## 模式、阈值和备用网关

| 模式 | 行为 |
| --- | --- |
| gateway | 始终使用配置的网关/已验证备用网关，不使用 Direct |
| direct | 只使用已验证 Direct；无地址或验证失败时拒绝新连接，不隐式走 Gateway |
| auto | 初始 Gateway；Direct 连续验证成功后优先使用；连续明确失败后回退 Gateway，恢复且冷却结束后切回 |

`fallback_gateways` 可配置最多四个 Endpoint，字段与 gateway 相同。备用网关也需完成真实 SOCKS5 转发验证；全部不可用时连接失败，但不会导致 AriNode 退出或改为香港本机直出。

- interval 默认 60 秒，范围 5–86400；检测完成后等待该间隔，不堆积重叠任务。
- timeout 默认 5 秒，范围 1–30；retries 默认 0，范围 0–3，示例使用 1。
- failure_threshold 默认 3、success_threshold 默认 2，范围 1–100。
- cooldown 默认 300 秒；0 表示采用默认值。恢复/提升受冷却约束；达到失败阈值的安全回退不等待冷却。
- dns_ttl 默认 60 秒，范围 5–3600；网关域名解析被缓存，过期后更新，支持 IPv4/IPv6 和多地址失败重试。
- tag 在本实例内唯一；最多 32 个 DSOM 出站。每个 native 节点仍使用 AriNode 自己的 outbound namespace。若要给不同节点不同住宅代理，使用不同的全局 DSOM tag。

出口历史保存在 `DSOMStatePath/<tag>/history.json`，默认根目录 `/var/lib/arinode/dsom`，最多保留 100 条。状态目录需可写且持久化；不要与其他配置文件共用同一路径。

探测与数据流分开调度，最多同时检测四个上游，每上游最多八个查询源，超时/重试次数有上限。配置热更新保留同 tag 的中继端口和已有连接。删除 tag 或停止服务会关闭其连接；网络对端主动断开时不能保证原连接继续存在。

未匹配 DSOM 的出站不经过该中继。DSOM 流量增加一次本地 TCP 跳转和数据复制，有额外开销；本版没有进行香港生产线路的吞吐压测，不声称零性能损耗。HTTP 查询不放在每包或每连接的数据转发路径中。

## 探测源、IPv6 和元数据

默认查询源为 HTTPS `api.ipify.org` 和 `ipv4.icanhazip.com`。可配置 `health_check.sources`：

```json
"sources": [
  {"url":"https://api.ipify.org"},
  {"url":"https://ipv4.icanhazip.com"}
]
```

纯文本响应必须只含一个可公开路由的 IP。JSON 源用 `json_field` 指定顶层字符串字段，例如 `{"url":"https://your-trusted-source.example/ip","json_field":"ip"}`。最多读取 8 KiB；拒绝非法地址、私有/回环/链路本地/CGNAT/文档测试地址。Internet 查询源必须 HTTPS 且验证证书，仅回环 HTTP 用于本地测试。不同源必须有不同 origin。

IPv4、IPv6 分别投票：同一地址至少达到 min_consensus（默认 2）且获得该地址族有效响应的严格多数，才更新已确认出口。IPv6 可选择供应商支持的两个独立 IPv6 查询服务，例如 api6.ipify.org 与 v6.ident.me；启用前自行确认查询源可用。一个成功、一个失败或票数不一致时保留上次确认值，不把它当成出口变更，也不把单源故障当作代理故障。新 Direct 没有共识不会被激活。

若供应商每条 TCP 连接随机换住宅出口，多源查询可能长期不一致；需要供应商的 sticky session，不能把每次响应当成同一台住宅设备。通过查询源看到的 IP 也不能保证任意网站连接使用相同出口。

若某出站无法完成全部查询，而独立配置的其他出站同轮完成多源验证，才将其计为转发链路失败；全部查询源共同异常时不据此误切换。只有 SOCKS 握手成功、但实际 HTTP 转发失败的已用 Direct，也能据此达到失败阈值后回退。

`metadata.url` 可选，支持 ipwho.is 兼容 JSON。返回 IP 必须匹配查询 IP；国别、ASN 和网络分类均标注 provider_reported，缺失或无效则 unknown。成功结果缓存 24 小时，失败缓存 1 小时；元数据失败不影响路由。默认不配置就不请求地理服务。网络类型不从 ASN 名称臆测，住宅分类也不是可直连住宅设备的证明。[接口格式](https://ipwhois.io/documentation)。

第三方元数据接口有额度和可用性限制；429 等错误显示 unknown，不作为切换依据。

## XBoard 路由：不需要面板新增 dynamic_outbounds 字段

本地 DSOM 注册 `bd-residential` 后，原有普通 routes、custom_routes、custom_route_rules 和 proxy_tag 可引用它。不要再在 panel custom_outbounds 中定义同名 tag；冲突会报错，不覆盖任一配置。

最兼容的方式是在 XBoard 路由管理中填写域名，动作选择“转发”，OUTBOUND TAG 填 `bd-residential`，再把规则绑定到香港节点。[普通 routes 下发示例](../example/dsom-xboard-route.json)。面板同步仍走原 API/插件配置接口；重复下发和 304 不会重置 DSOM 状态，DSOM 配置变更也能在面板 304 时更新本地策略。

也可只用本地节点 Options 的 `DynamicRoutes`：

```json
"DynamicRoutes": [{
  "name":"ai-via-bd",
  "match":{"domain_suffixes":["claude.com","claude.ai","anthropic.com","openai.com"]},
  "action":{"type":"route","target":"bd-residential"}
}]
```

DynamicRoutes 复用同一条 normalized rule 编译链，位于面板 structured rules 前；现有内核级 local override 仍保留原优先级。支持当前内核的其他匹配字段，无需再实现一套 parser。不匹配上述路由的流量继续按既有本机 direct / 面板策略处理。

多级代理链可继续把另一个 custom outbound 的 `proxy_tag` 指向 `bd-residential`。本地中继只支持 SOCKS5 CONNECT/TCP，UDP ASSOCIATE 会明确拒绝；匹配 DSOM 的 UDP 不会由模块兜底走香港 direct。

## 状态和链路诊断

```bash
anctl dsom status
# 或使用已有受保护的状态接口；非回环监听仍要求 ARINODE_STATUS_TOKEN。
curl -fsS http://127.0.0.1:18086/v1/status

# 必须在香港入口运行，才能测到香港到供应商网关的连接表现。
anctl dsom diagnose --host kochab.srvgdx.com --port 10374 --attempts 20 --timeout 5
# 可选：系统已有可用的 Linux iputils ping 时测 ICMP 丢包/RTT。
anctl dsom diagnose --host kochab.srvgdx.com --port 10374 --attempts 20 --timeout 5 --icmp
```

状态接口增加 `dynamic_outbounds`，包含 mode、active、active_address、gateway、exit_ips、metadata、direct_state/direct_reason、last_check、last_switch、last_failure、probe_latency_ms 和 history。`exit_fresh=false` 表示 exit_ips 只是上一次确认的值，`exit_confirmed_at` 给出确认时间，不能误认为本轮探测成功。网关 DNS IP 与实际公网出口分开展示，住宅设备地址不作推断。日志仅打印 tag、模式切换、已确认的出口 IP。

TCP 连接耗时/失败率不是 ICMP 丢包率；probe_latency_ms 还包含认证与查询服务的响应时间，不代表纯网络 RTT。`--icmp` 不提权、不安装 ping；不可用时明确返回 unknown/unavailable，不编造丢包数字。ICMP 被过滤或降优先级也不能证明 SOCKS5 不可用。[iputils ping 文档](https://github.com/iputils/iputils/blob/master/doc/ping.xml)。

## 验证与边界

自动测试使用真正进行 CONNECT、认证和 HTTP 转发的本地 SOCKS5/查询源，覆盖固定出口、IP 变化、IPv6、未配置 Direct、仅 TCP 可达、认证/转发失败、自动回退/恢复/冷却、地址/凭据文件轮换、结果不一致、历史上限、DNS TTL、元数据错误、优雅停止、面板 304/重同步和本地原子保存热更新。

Xray/sing-box 集成测试验证 VLESS 用户流量实际经过 DSOM 与多级 SOCKS 链，热换网关后新连接走新地址，已有连接仍可收发，native outbound 和 inbound 不重建。

```bash
GOEXPERIMENT=jsonv2 go test -p 2 ./...
GOEXPERIMENT=jsonv2 go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./...
GOEXPERIMENT=jsonv2 go test -race ./dsom ./conf
```

本次没有供应商 SOCKS5 凭据、明确授权的住宅代理 host:port 或香港入口访问权限，因此没有宣称该供应商的住宅设备已能直接访问，也没有对历史出口执行任何端口探测。只有当供应商提供可达代理服务或可控隧道后，Direct 才具备实际启用条件。NAT/反向隧道/私有住宅网络不能靠动态 IP 探测绕过。

临时回退可把 mode 改为 gateway，继续使用已配置供应商网关。程序降级需同时恢复升级前的本地配置和面板路由；旧 agent 不识别 DynamicOutbounds，不应直接沿用引用动态 tag 的新路由。
