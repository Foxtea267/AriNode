# HY1 / VLESS 用户热更新、撤销与计费

本轮按截图中的具体问题核查 AriNode 当前源码及固定依赖。截图对“数组位置不能作为持久认证身份”的判断适用于当前 HY1；VLESS 也有相似的位置映射和连接撤销问题。不能据此推断某个后端没有其他 bug，或只换成面板 UID 就能解决 UUID 重置后的旧连接授权。

## 复现与根因

### Hysteria v1

固定的 sing-box fork：

- `protocol/hysteria/hysteria_user.go` 删除用户后压缩 `userNameList`，重新生成 `0..n-1`。
- `protocol/hysteria/inbound.go` 在新建 TCP/UDP 流时用认证 context 的整数索引当前 `userNameList`。
- 固定 sing-quic 的 `hysteria/service.go` 在 QUIC 初次认证时保存 `serverSession.authUser`；后续所有子流继续使用它。

删除前位用户后，旧 QUIC parent 的整数不变，而名单已经移动。旧用户可能被算给另一用户，末位旧整数也可能越界。面板 ID 稳定仍不足以处理“同 ID 重置 UUID”或“同 UUID 删除再添加”：旧 parent 不应因新用户记录恰好复用 ID 而恢复授权。

隔离的未修改 HEAD `9a6d68ee` 只复制新测试，真实 HY1 出现：

```text
UUID reset left the old stream forwarding
revoked authentication opened a new stream on its old QUIC transport
revoked authentication opened a new stream on its old QUIC transport
FAIL
```

完整现场 `.tools/hy1-revocation-before.log`，没有将“可能越界”说成该次测试实际发生的 panic。

### VLESS

固定 fork 的 `protocol/vless/inbound.go` 在认证前执行 `userconns.Store(conn, metadata.User)`。此时用户字段通常为空；认证回调只更新另一份局部 metadata，没有修正登记。`vless_user.go:DelUsers` 按 UUID/name 查 map，自然匹配不到活跃连接。

同时，SDK 缓存的是整数身份，后续回调查可变 `h.users[userIndex]`。删掉前位用户后，已认证 mux 的延迟子流仍可能使用旧 index。隔离真实测试曾出现：

```text
panic: runtime error: index out of range [1] with length 1
```

AriNode 原来的 `core/sing/user.go:DelUsers` 随后删除 counter 和 UUID→UID；活着的连接仍握着旧 TrafficStorage，已发生及继续转发的流量不再进入 collector。

TCP、WebSocket、HTTPUpgrade 三种真实回归在修复前均失败：

```text
active user's pre-revocation bytes were lost or misattributed
revoked VLESS TCP stream remained active
revoked VLESS UDP association remained active
re-adding a UUID revived its revoked TCP stream
re-adding a UUID revived its revoked UDP association
```

完整现场 `.tools/user-revocation-before.log`。这些测试没有额外 SOCKS 出站，也不依赖面板数据或生产服务器。

## 实现

- **HY1 复用 QUIC 适配层。** `core/sing/quic_inbound.go` 注册本地 HY1，SDK 的用户泛型改用不可复用的 `*quicUser` 代际记录。认证表仅在未发布的 SDK 快照中构建；健康旧 parent 保留原快照。每次路由检查记录是否仍为当前授权，删除时关闭所属 TCP/UDP 转发。
- **VLESS 使用本地稳定身份适配。** `core/sing/vless_inbound.go` 用不可变 `Service[*stableVLESSUser]`，跟踪认证后的父连接和子连接，删除时关闭；未变化用户保留代际。mux、UoT 内层再次核对身份并登记连接，旧连接不能因同 UUID 重加而恢复。
- **晚到路由仍需原子授权。** 嗅探或 DNS 等待可能让路由在撤销后才进入 Hook。VLESS 通过私有 context 租约，在代际锁内完成身份检查及 counter 获取；要么取得将被退休的原 counter，要么拒绝。未增加其他协议的通用撤销架构。
- **VLESS 尾部流量保留原 UID。** `TrafficStorage.RetireToFunc` 将原计数和晚到复制回调导向按 UID 暂存的 spool。上报 drain 后删除已报 pending 项，晚到字节可重新插入；不永久保存每个历史 UID。重加同 UUID 换 UID 不会串账；相同 UID 的旧尾部和新活跃流量在 collector 中合并成一行，避免 panel push 的 map 覆盖。
- **并发首连接计数。** Hook 为节点创建 TrafficCounter 改用 `LoadOrStore`，避免多个并发首连接创建不同容器后互相覆盖。

保留固定依赖、面板格式、限速共享桶、设备跟踪及 Mieru 限速握手修复。HY1 保留带宽 Up/Down 与 Mbps 优先级、obfs、UDP timeout、legacy receive windows、MaxConnClient 和 MTU 选项。VLESS 保留 TLS/Reality/Vision、TCP/ws/grpc/h2/HTTPUpgrade、mux/UoT/packetaddr；没有在 Vision 前添加遮挡 TLS 接口的连接包装。

相关生产文件：`core/sing/quic_inbound.go`、`user.go`、`vless_inbound.go`、`httpupgrade.go`、`hook.go`、`sing.go`、`node.go`，以及 `common/counter/traffic.go`、`conn.go`。已有 QUIC、计数、上报和面板 TLS 改动均保留，没有升级 `go.mod/go.sum`。

## 测试与命令

环境为 Windows amd64、Go1.25.0、GOEXPERIMENT=jsonv2、GOMAXPROCS=2；实际命令使用 `.tools/go-dsom/go/bin/go.exe`。

```sh
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 -v \
  -tags 'with_quic with_grpc' ./common/counter ./core/sing \
  -run 'TestRetired|TestVLESS|TestQUIC|TestHY1|TestHook' -count=1 -timeout=180s

GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 \
  -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' \
  ./... -count=1 -timeout=240s
```

实际验证包括：

- HY1/HY2/TUIC 的白名单热变更、UUID 重置、旧 QUIC parent 开新流拒绝、删后重加、活跃 UDP、空名单、健康用户及多节点隔离、DelNode。
- VLESS TCP/ws/HTTPUpgrade 的真实 TCP+UDP，单次上传 **128 B**、下载 **512 B**，累计 **256/1024 B**；空闲用户不增加流量；撤销后准确保留计数，重复 drain 不重放；新认证恢复而旧连接不恢复。
- VLESS 已认证健康 mux 延迟建立首个 UDP 子流，避免仅测试删除前已经完成路由的流。
- VLESS 捕获旧认证快照、并发更新/认证、重复 close callback，以及可控“路由等待→删除→重加→进入真实 Hook”TCP/UDP 测试。
- 原始 counter 关闭后晚到 Copy CountFunc，退休与多生产/多 collector 并发；spool drain 后晚到字节、历史 UID 回收、同 UID 合并与不同 UID 隔离。

组合回归已通过，`.tools/hy1-vless-counter-final.log`：counter 1.843s、sing 8.291s。第一次完整 release 标签测试已全部通过，`.tools/hy1-vless-full.log`：sing 51.482s。spool 四项测试连续 3 次通过，`.tools/vless-retired-traffic-tests.log` 1.576s。交付前最终结果见本节末尾补充记录。

最终 targeted 组合 `.tools/vless-retired-and-real-final.log` 全通过（4.850s），包含真实 VLESS、HY1/HY2/TUIC、退休计数及延迟 Hook 租约。新增 `core/sing/hy1_obfs_test.go` 的真实 HY1 XPlus 混淆回归通过（test 0.24s/package 1.952s，`.tools/hy1-obfs-integration.log`）：更新认证快照后健康 TCP/UDP 继续、被删用户旧流及旧 parent 新流拒绝、新用户新 parent 成功。

Linux amd64/arm64、CGO0、jsonv2、完整 release 标签的服务程序交叉编译均通过，日志 `.tools/hy1-vless-build.log`。产物仅保存在工作区测试目录，没有发布、安装或运行生产服务。

交付前最终完整测试 **全部通过**，完整输出 `.tools/hy1-vless-full-delivery.log`：`api/panel 9.156s`、`common/counter 0.065s`、`common/rate 8.575s`、`core/sing 31.588s`、`node 3.699s`。使用上述完整 release 标签、`-count=1 -timeout=240s`，包含新增 HY1 混淆和退休流量测试，没有删减已有回归。`gofmt` 和 `git diff --check` 已完成。

本机没有 GCC，不能运行 race；CI 加入 VLESS 稳定状态机和退休计数测试，尚不代表远程 CI 已运行。固定 sing-quic 另有 pooled packet session ID callback 的并发风险，保留原说明；没有用认证状态机测试冒充真实 SDK packet path 无竞态。

实际 race 命令为 `CGO_ENABLED=1 GOEXPERIMENT=jsonv2 go test -race -p 2 ./common/counter ./core/sing -run 'TestRetired|TestVLESSStable' -count=1 -timeout=120s`；失败输出保存在 `.tools/hy1-vless-race.log`，错误为 `cgo: C compiler "gcc" not found`。

## 剩余边界

1. VMess、Trojan、经典 Shadowsocks、Xray 和独立 Hysteria2 的修复及尚未覆盖的 SS2022 等边界，见[后续审计](user-revocation-followup.md)。
3. **上报失败与 ACK 丢失不同。** 既有内存 pending report 修复保留失败批次；原生 XBoard push 无幂等报告 ID，面板已经计费但 ACK 丢失仍可能重复提交。恰好一次需要服务端去重，不能靠本次身份修复保证。进程退出仍会丢内存待报数据。
4. **撤销以成功同步到有效白名单为前提。** 面板 API 未下发到期时间，失联时继续保留最后有效授权；不承诺到期瞬间断开。
5. **节点移除与进程关闭不是持久化计费日志。** spool 按运行节点存活；DelNode/退出不保证尚未提交数据保留。该边界未扩大为跨重启持久化工程。
6. 本机真实协议回环测试不等于生产公网、所有客户端与所有配置验收。面板 API 的现场 ECDSA 问题也没有因此得到根因结论。

本轮没有连接生产、部署、替换生产二进制、重启服务、修改面板/节点配置或数据，也没有推送触发自动发布。
