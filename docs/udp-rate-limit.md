# UDP 用户限速

默认 `sing` 内核现在在普通 UDP 转发路径应用用户限速桶。此前 `HookServer.RoutedPacketConnection` 调用了 `CheckLimit`，但应用返回桶的代码被注释，导致用户正数 `speed_limit` 对 UDP payload 不生效。这是与 Mieru 握手接口透传不同的缺陷。

## 使用与单位

继续在 XBoard 设置用户 `speed_limit`，无需增加出站、修改节点配置或清空速度字段。正数单位仍为十进制 Mbps：`300` 对应 `37,500,000 B/s`。节点、用户及动态限速的原有合并规则不变；无限速用户没有额外的包限速包装层。

同一节点、同一用户的 TCP、UDP、上下行及并发连接使用同一个现有限速桶，合计消耗额度。桶的容量、补充周期与首次突发行为不变，不是每条连接各获得完整速度额度。不同节点和不同用户的桶仍隔离；多台服务器的额度独立，不是跨服务器分布式限速。

这里限制的是代理转发 payload 字节，不包含 IP、UDP、QUIC、TLS 头和底层重传开销，不能当作物理网卡带宽上限。UDP 没有 TCP 的端到端背压能力，超负载持续发送时底层队列可能丢包；本实现不会为每个包创建后台 goroutine 或无界缓冲队列。

## 实现

- `common/rate/packet.go`：只包装 sing 的 `network.PacketConn`，读取成功后、把回包交给底层发送之前预留并等待 payload 对应的令牌。
- `core/sing/hook.go`：使用 `CheckLimit` 返回的原用户桶，不创建另一套桶；外层继续使用现有流量计数及设备生命周期包装器。
- `Upstream()` 保留握手、NAT、headroom 等控制接口查找；`ReaderReplaceable()` / `WriterReplaceable()` 均为 `false`，阻止 sing 的缓存、waiter、原始 socket 或优化复制路径绕过限速。
- `Close()` 中断当前包装层的桶等待，并只关闭一次底层连接。取消的令牌预留不退款，避免与同用户其他会话产生竞争。socket deadline 继续透传，但不会单独中断桶等待；需要取消等待时关闭包装连接。

保留原有用户同步、撤销、计费、设备限制和 TCP `Read/Write` 算法。不修改面板数据或协议依赖。

## 覆盖范围与已知限制

| 路径 | 用户限速情况 |
| --- | --- |
| `sing` 普通 TCP 转发 | 原有限速行为保留 |
| `sing` 普通 UDP 转发 | 本次补齐上下行共享桶；包括 Mieru、VLESS、VMess、SS、Trojan、HY1/HY2、TUIC、AnyTLS 的 packet tracker 路径 |
| `sing` 内部 `hijack-dns` | 依赖在 packet tracker 之前处理，不经过该限速包装层；不属于普通 UDP outbound 转发 |
| Xray 普通 `Dispatch` | 现有双向 writer 已应用用户桶，TCP/UDP 都覆盖，本次未修改 |
| Xray `DispatchLink` | 已知独立问题：下行 writer 应用桶，上行 reader 未应用；同时涉及 TCP/UDP，本次未修改 |
| 独立 `hysteria2` 内核（`core/hy2`） | 已知独立问题：只使用节点协议带宽，日志准入检查未消费用户桶，不能视为落实用户 `speed_limit`；本次未修改 |

`sing` 的 Hysteria2 与独立 `hysteria2` 内核是不同实现。本文不表示所有内核、内部 DNS 操作或所有协议参数组合都已限速。现有 TCP 计数器普通 `Read/Write` 使用 `Store` 的独立累计问题也没有混入此补丁；共享 TCP/UDP 计数回归使用真实的 extended copy 计数路径。

## 回归验证

- 包包装器：直接读写、`counter → rate → cached packet` 复制、缓存首包、优化接口防绕过、IPv4/IPv6/域名目的地址、内容完整、底层错误及 Close 取消等待。
- Hook：同用户一个 TCP 与两个并发 UDP 会话共享桶，另一个用户隔离；双向精确累计、计费快照与清零、设备去重及关闭释放。
- Mieru：TCP/UDP 两种底层传输，各测无限速和 `speed_limit=300`；限速场景额外用预先耗尽的 `512 B/s` 测试桶，验证实际 UDP payload 往返耗时和非零双向计数。原有失败握手、用户撤销及健康用户隔离仍保留。
- 其他协议：VLESS、SS、HY2、TUIC、AnyTLS 的真实本机 UDP 认证转发及限速测量。

```sh
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 -v ./common/rate ./limiter ./core/sing -count=1 -timeout=240s
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./... -count=1 -timeout=240s
```

测试桶仅存在于测试代码，生产仍使用用户实际配置的桶。本机回环与 CI 测试不代表生产链路已验收。发布新版本不会自动部署到生产；是否升级由管理员决定，自动升级默认关闭。

### 本次本机验证记录

环境：Windows amd64，Go 1.25.0，`GOEXPERIMENT=jsonv2`、`GOMAXPROCS=2`，测试并行参数 `-p 2`。使用仓库自带依赖版本，未修改 `go.mod/go.sum`。

先添加真实 Mieru UDP 限速断言、尚未修改生产代码时：

```text
go test -p 2 -v ./core/sing -run '^TestMieruAuthenticatedForwarding/tcp_limited$' -count=1 -timeout=90s
FAIL: real Mieru UDP round trip bypassed the shared 512 B/s bucket:
128 bytes each way in 52µs
```

修复后：

| 验证命令/场景 | 本机结果 |
| --- | --- |
| `go test -p 2 -v ./common/rate ./limiter ./core/sing -count=1 -timeout=240s` | 三个包全部通过；rate 8.572s，limiter 0.056s，sing 38.868s |
| Mieru `tcp_unlimited` | 通过，2.03s，含失败握手、UDP association、撤销及隔离 |
| Mieru `tcp_limited` | 通过，16.06s，含真实 300Mbps 握手和低速 TCP/UDP 转发 |
| Mieru `udp_unlimited` | 通过，13.04s，含失败握手、UDP association、撤销及隔离 |
| Mieru `udp_limited` | 通过，4.05s，含真实 300Mbps 握手和低速 TCP/UDP 转发 |
| 带正式标签的 `go test -p 2 -v ./core/sing -run '^TestAllSingProtocolsAuthenticatedForwarding/.*-udp-limited$' -count=1 -timeout=120s` | VLESS、SS、HY2、TUIC、AnyTLS 五个场景通过；128B 双向 payload 的桶等待约 500–507ms |
| 带正式标签的 `go test -p 2 ./... -count=1 -timeout=240s` | 全部包通过，sing 37.050s |
| Linux amd64、arm64 的 `arinode` 与 `anctl` | 四个构建通过 |

正式标签均为上方完整标签字符串。交叉构建采用 `GOOS=linux GOARCH=amd64/arm64 CGO_ENABLED=0 go build -buildvcs=false -p 2 -trimpath -tags '<正式标签>'`，分别构建 `.` 与 `./cmd/anctl`。本机没有 C 编译器，未运行 race；CI 增加了 `common/rate` 和共享桶/设备 Hook 的 race 测试作为发布前门禁。测试场景总耗时还包含连接清理及撤销，不能当作握手耗时；实际握手另有小于 5 秒断言。
