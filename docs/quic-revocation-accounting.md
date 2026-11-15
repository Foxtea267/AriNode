# HY2/TUIC 撤销与流量计数修复

本次对 `20261113-9a6d68ee` 进行本机源码审计和真实协议回归。旧版确实存在认证撤销与计数缺陷，不能从“协议能连接、限速测试通过”推断用户撤销和计费都正确。

## 复现与根因

| 问题 | 旧版行为 | 根因与修复 |
| --- | --- | --- |
| 重置 UUID，旧 QUIC 连接开新流 | HY2/TUIC 均能复现 | SDK 在认证后保留数字 UID，inbound 每次开流查当前 UID → UUID；相同 UID 被重用后，旧连接会映射到新凭据。改用不可复用的认证记录指针，每次 TCP/UDP 路由前核对当前记录 |
| 删除 HY2 用户，已有 UDP 仍转发 | 能复现，空用户名单也不能停止已有 UDP | 原 inbound 只登记 TCP，未登记包连接；现在按节点统一登记并关闭两种 `io.Closer` |
| 删除有活跃 UDP 的 TUIC 用户 | `*canceler.TimeoutPacketConn is not net.Conn` panic | 原依赖把包连接键强转成 `net.Conn`；本地适配层使用共同的关闭接口 |
| TCP 流量累计被覆盖 | 重复读写、EOF、部分成功后出错均能复现 | `ConnCounter.Read/Write` 使用 `Store(n)`；改为有正数字节时 `Add(n)`，保留已传输字节及原始错误 |
| 取数后清零丢并发增量 | `Load` 后再 `Store(0)` 存在窗口 | 各内核 collector 改用原子 `Swap(0)` 获取该方向待上报总量；阈值与用户归属不变 |
| 面板上报失败丢待报流量 | core 已清零，HTTP 失败后未保留数据 | 按 UID 暂存失败批次，仅合并新取出的字节；下一轮重试，收到成功响应后清空 |

白名单变化后流量归属的对照测试在旧版和新版均通过：一次上传 **128 B**、下载 **512 B**，另一个空闲用户计数为 **0**。未把没有复现的归属错误列作修复成果。

## 实现范围

`core/sing/quic_inbound.go` 通过现有 registry 注册本地 HY2/TUIC 适配层，继续使用仓库固定的 sing-quic SDK，不修改 `go.mod/go.sum` 或协议依赖版本。

认证表构建后保持不可变。用户变化时构建新 SDK 实例，实际 UDP/QUIC listener 由 inbound 独占，只将新的 QUIC 连接交给新实例，不重新绑定监听端口。既有健康连接继续使用原 SDK 实例，原实例在最后一个 parent 结束后释放。这样避免在认证进行中写 SDK 用户 map，也保留其他用户连接。

同名用户删除再添加会获得新认证记录，旧 transport 即使仍处于 QUIC idle 生命周期，也不能恢复代理转发或新建获授权的流。撤销关闭已登记的 TCP/UDP 转发连接；删除节点或关闭 core 还会关闭该 inbound 的 parent QUIC 连接。节点间相同 UUID 的认证和计费身份独立。

原有端口、TLS、HY2 带宽与 salamander、TUIC 拥塞控制/heartbeat/0-RTT/UDP timeout、路由、用户共享限速桶与计费接口继续使用原配置。协议编解码仍由原 SDK 处理。

计数修复位于 `common/counter`，sing、Xray、独立 HY2 的流量读取接口均改为原子 drain。两个方向独立取数，不是跨方向事务；并发增加的字节会出现在本次或下一次快照中。

## 边界

1. **到期依赖面板成功返回新用户名单。** UniProxy 用户接口没有订阅到期时间。轮询最多间隔 30 秒，另有面板缓存和网络延迟。面板持续失联时仍保留最后有效授权，不能承诺到期瞬间断开。
2. **原生流量 push 没有幂等键。** 失败批次保留后属于至少一次发送：面板已成功记账、但成功响应丢失时，重试仍可能重复计费。恰好一次效果需要面板按稳定报告 ID 去重；本次不修改面板源码。已有 cluster 报告 ID 机制保留。
3. **失败批次暂存在进程内存。** 正常重试保留数据；进程退出、崩溃、强制重启可能丢失尚未确认的数据。本次没有加入持久化日志。
4. QUIC 认证修复覆盖默认 **sing** 的 HY2/TUIC。独立 `hysteria2` 内核的认证隔离、Xray `DispatchLink` 上行限速见[后续审计](user-revocation-followup.md)；独立内核仍未执行用户带宽限速。
5. 固定版 `sing-quic v0.6.0-beta.4` 自身另有经源码确认、但本机未能动态 race 检验的并发风险：HY2/TUIC 的 packet session 清理闭包捕获池化 `udpMessage.sessionID`，包释放会将整个 message 清零并放回池中，之后关闭闭包可能读取已清零或复用的 ID。race 任务未执行；本次隔离 race gate 避免进入 SDK 的真实 QUIC packet callback，只验证 AriNode 自己的认证状态机。没有修改或升级依赖，因此不能声称该 SDK 并发路径已无竞态。完整真实 QUIC/UDP 功能测试仍运行于普通全量测试。
6. 本机真实回环测试与 CI 不等于生产公网链路验收，不能据此宣称所有协议、所有配置完全无缺陷。

## 回归测试

旧版通过 `git archive` 隔离，运行同一新测试，保留以下失败：

```text
revoked authentication opened a new stream on its old QUIC transport
revoked user's existing QUIC UDP association still forwards
empty user snapshot left healthy UDP association forwarding
DelUsers panicked with active QUIC sessions:
interface conversion: *canceler.TimeoutPacketConn is not net.Conn: missing method Read
```

新测试包含：

- `core/sing/quic_revocation_test.go`：HY2/TUIC 白名单变化归属、UUID reset/readd、活跃 UDP 撤销、空快照、健康用户与多节点隔离、DelNode。客户端首次认证后阻止建立替代 transport，确实验证旧 QUIC 连接。
- `core/sing/quic_inbound_test.go`：代际授权、无变化不替换快照、并发用户更新/认证登记、TCP/UDP 关闭、路由回调释放、真实 parent 引用和旧 SDK 释放生命周期。
- `common/counter`：重复读写、EOF、部分成功伴随错误、普通/extended/copy 路径累计、共享计数器并发连接、多 producer/collector 原子 drain。
- `core/xray/traffic_snapshot_test.go`、`core/hy2/traffic_snapshot_test.go`：真实 collector 阈值、清零、新增数据、用户/节点隔离。
- `node/traffic_report_test.go`：多次失败后合并新流量、core 取数失败仍重试、重叠上报串行、重试不被当作新在线活动。

```sh
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 -v -tags 'with_quic with_grpc' ./core/sing -run '^TestQUIC' -count=1 -timeout=120s
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 ./common/counter ./common/rate ./limiter ./node ./api/panel -count=1 -timeout=240s
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./... -count=1 -timeout=240s
```

CI 另运行 counter、QUIC 认证状态机及上报重试的 race 测试，所有门禁成功后才生成正式包。真实 QUIC packet race 因上述固定版依赖问题未运行，不能用隔离的认证状态机 race 结果替代。正式包不自动部署；本次不操作生产服务器、面板数据或节点配置。
