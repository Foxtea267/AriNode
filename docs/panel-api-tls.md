# 面板 API TLS 故障：审计、诊断与隔离验证

本轮只修改源码和隔离测试。**没有复现现场常驻 AriNode 的 ECDSA 验证失败，不能宣称已修复该生产根因。** 补丁修复了有证据的错误日志凭据泄露和在线设备缓存提前清空，补充严格 TLS 诊断；不改变证书信任策略。

## 版本与证据边界

| 项目 | 现场提供的信息 / 本机实际核查 |
| --- | --- |
| 生产参考源码 | `20261030-5e0fd9ab`，`5e0fd9ab5d19f9f3a6d6871b88048f39cf5932b7` |
| 生产二进制 SHA-256 | 用户提供 `eddfa8f3cd0ce120d29887f39e61a5a88dc609523c37884a707015d1ce5264dd`；本机没有该二进制，未验证它与参考源码的构建对应关系 |
| 本轮基准 | `9a6d68ee523e060d5e72c4446933e67d3e592f65`，另保留工作区原有 QUIC 撤销、计数和上报修复 |
| 生产构建参数 | 用户提供 Go 1.25.14、`GOEXPERIMENT=jsonv2`、`CGO_ENABLED=0`、linux/amd64、GOAMD64=v1 |
| 本机工具链 | Go 1.25.0 windows/amd64，启用 jsonv2；不能替代 Go 1.25.14/1.26.2 等价对照 |

已直接读取生产参考 commit 的 `api/panel/panel.go`，并比较到当前基准的历史：DSOM、本地策略合并、机器注册和自动发现、后端更新等功能已加入。Resty 仍为 **2.16.5**，Go 模块仍要求 **1.25 / toolchain go1.25.0**；`go.sum` 没有变化，`go.mod` 的差别仅为已有 flock 依赖从 indirect 移到 direct。不能直接用整个当前 HEAD 代表生产版本的最小修复。

用户提供的 OpenSSL、Python、独立 Go 和离线链验证成功，说明当前证书链与新进程可以通过严格验证。它们没有还原故障进程的完整初始化、内存状态和连接历史；尚不能证明系统根缓存、共享对象污染、ECDSA 实现、CA 版本或边缘节点中的哪一项导致现场失败。

## 请求路径与对象生命周期

| 路径 | 实现与复用方式 |
| --- | --- |
| 原生 config/user/alivelist/push/alive/status | `api/panel/panel.go` 创建每个绑定的 Resty client；`node` controller 周期复用。旧面板走 `/api/v1/server/UniProxy/`，machine 节点走 `/api/v2/server/` |
| 插件配置 | `api/panel/config.go` 使用同一 Resty client；缺少插件时保留原生 API fallback |
| 机器发现 | `node/discovery.go` 每轮用 `panel.New` 建 client，`machine.go` POST `/api/v2/server/machine/nodes`；失败保留现有节点 |
| 机器心跳 | `status.go` 使用绑定 client，POST `/api/v2/server/machine/status` |
| Cluster | `cluster.go` 使用绑定 client 访问 `/api/v1/arinode/cluster/`；保留报告 ID 和确认机制 |
| 注册 | `enroll.go` 独立 `http.Client`，禁止重定向，标准 TLS 验证；未设置 Transport 时使用标准默认 transport |
| 后端更新任务 | `backend_update.go` 独立 `http.Client`，禁止重定向，标准 TLS 验证；错误原本已不携带原始 URL |

Resty 的 `resty.go:New` → `client.go:createClient` 在 Transport 为空时调用 `createTransport(nil)`。`NewWithLocalAddr` 也创建独立 Transport。因此**不能把 resty.New() 解释成共享 http.DefaultTransport**。测试确认默认和 APISendIP 两分支的四个 HTTP client/transport 互不相同，初始 TLSClientConfig 均为空。

生产参考版本和当前源码均未给面板 Resty 设置自定义 RootCAs、VerifyPeerCertificate 或 VerifyConnection，未发现修改全局 DefaultTransport 的路径。标准库系统根缓存仍属于可能的进程状态差异，尚无故障证据。

`common/cert` 的入站 PEM 归一化、解析和磁盘发布创建独立材料；未发现其与面板 Transport、CertPool、ECDSA 公钥或原始证书切片共享的路径。固定版本 Xray 的私有 TLS 代码会调用 SystemCertPool；该函数返回池副本，不能仅凭此认定污染了面板信任库。sing-box 的私有 TLS 回调也不等于修改面板 client。

## 已确认问题与最小修改

1. **原始请求错误会泄露查询参数。** 原生认证 token 在 query 中；Resty 的默认重试 logger 会将含 URL 的错误直接写 stderr，原来的 GetUserList/checkResponse 也可能向调用方返回它。现在关闭该原始 logger，统一输出白名单字段；Debug 不再打开完整请求/响应 dump，HTTP 错误不打印响应正文。用户和节点 JSON/msgpack 解码失败不再直接拼入原始非法字段值。
2. **在线设备缓存更新早于请求成功。** GetUserAlive 之前在发请求前替换 `Client.AliveMap`。隔离测试稳定出现 `TLS failure cleared the last valid alive cache`。现在在临时快照中解析并完成校验，成功后才发布 AliveMap/AliveIPs；TLS、网络、HTTP 和解析失败保留原状态。
3. **保留旧面板兼容。** 原生 alivelist 在尚无成功快照时返回 404/405，仍允许使用空设备计数启动；已有有效快照时返回错误并保留它。Cluster 不走此 fallback。403、5xx 和 TLS 错误不会被当成“接口不存在”。可选 richer devices 接口 HTTP/网络失败仍回退到本轮有效的原生计数。
4. **增加失败诊断，不修改信任。** 每次有界重试结束时输出错误分类、节点/机器 ID、面板地址、API 路径、SNI、DNS 结果、拨号/连接地址、根池来源、证书与 SPKI 的 SHA-256、签名算法和有效期、Go 版本及可用的构建参数。日志不包含 query、fragment、userinfo、请求体、响应体、证书 PEM 或私钥。

`SafePanelAddress` 保留合法 base path 和末尾斜线，避免无故改变正常配置的 runtime tag。query/fragment 不参与日志或生成的 tag；userinfo 地址显示占位符。正常认证请求及配置格式没有改变。

诊断示例（指纹省略）：

```text
Panel API request failed
error_kind=tls_unknown_authority
endpoint=/api/v2/server/config node_id=244 machine_id=39
tls_roots=system_default tls_server_name=backend.novonet.cc
last_dial_addr=<address:port> peer_cert_sha256=<fingerprint metadata>
go_version=go1.25.x go_os=linux go_arch=amd64
```

`last_dial_addr` 是最后一次拨号尝试，Happy Eyeballs 下不保证是最终连接地址。`remote_addr` 仅在 GotConn 得到连接后记录；TLS 握手失败通常没有此事件。`system_cert_pool_readable=true` 仅代表 SystemCertPool 返回非空池，不证明旧进程使用的候选根公钥正确。peer chain 摘要也不是实际选中的根证书摘要。日志不能独自证明生产根因。

## 离线回归与实际结果

本地 CA 均由测试生成，无公网证书依赖：P-256/P-384、多级链、Root YE 被 X2 交叉签名。有效链通过；陌生根、错误签名、域名不符、过期证书必须失败。

新增测试文件：

- `api/panel/tls_chain_test.go`：严格链校验、四个私有 transport、多面板根池隔离、5 周期 × 12 并发复用与叶证书轮换；同期执行真实入站 CanonicalPair/Parse/Store.Save/Load 轮换。测试为强制新握手而关闭 idle；生产没有加入每次关闭连接的行为。
- `api/panel/tls_recovery_test.go`：真实 config/user/alive/discovery/status/traffic API，可信 → 不可信 → 可信；失败时保留 bytes/hash/ETag/设备缓存，恢复后 304 和后续新 revision 正常同步。握手失败期间 HTTP handler 收到 0 次请求，恢复后测试流量 128 B 上行、512 B 下行只收到一次。
- `api/panel/error_sanitization_test.go`：在构造 client 前捕获真实 os.Stderr，并以原生 Resty logger 做对照；默认 3 次 retry 共 4 attempts 不泄露凭据，Debug 无完整 dump，非法响应字段值不进入相关错误。
- `api/panel/devices_test.go`：可选设备接口 HTTP503 与真实 EOF fallback，失败不替换成功快照，旧端点兼容。
- `node/panel_tls_discovery_test.go`：真实 TLS 拒绝后，正在运行的 controller、binding 和 core 保留。
- `api/panel/tls_dependencies_linux_test.go`：Linux 外部 package，通过子进程启动前设置临时 CA bundle，加载 release tags 的 core/imports，使用正常 panel.New 的系统根路径验证发现失败/恢复。**本机只能交叉编译，未执行此 Linux 测试。** 也没有启动全部内核或模拟小时级长跑。

实际命令使用 `.tools/go-dsom/go/bin/go.exe`，环境 GOEXPERIMENT=jsonv2、GOMAXPROCS=2、GOTOOLCHAIN=local，模块/cache 目录位于工作区。

```sh
go test -p 2 -v ./api/panel ./node -count=1 -timeout=240s
go test -p 2 -v ./api/panel -run '^TestPanelTLSRecoveryPreservesSnapshotsAndReportsOnce$' -count=1 -timeout=120s
go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./... -count=1 -timeout=240s
CGO_ENABLED=1 go test -race -p 2 ./api/panel ./node -count=1 -timeout=240s
```

恢复测试修复前失败、修复后通过，分别保留 `.tools/panel-tls-recovery-before.log` 和 `panel-tls-recovery-after.log`。面板/node 完整包测试通过，日志 `panel-tls-targeted-final.log`。完整 release 标签测试最终通过，日志 `panel-tls-full-final.log`。

此前完整回归曾出现 `TestAllSingProtocolsAuthenticatedForwarding/trojan-httpupgrade` 的 `read destination: EOF`。单项重复 5 次有 1 次失败；未加入当前工作区改动的 HEAD 归档对照重复 10 次有 8 次 EOF，2 次通过，分别保留 `panel-tls-trojan-repeat.log`、`panel-tls-trojan-baseline.log`。最终一次全通过不代表此不稳定场景已被解决；未删除断言或修改协议测试。

race 未能运行：本机缺少 C 编译器，实际输出为 `cgo: C compiler "gcc" not found`，记录于 `panel-tls-race-final.log`；没有以普通并发测试替代 race 结论。

独立补丁在干净 HEAD 归档中应用并核对内容后，单独运行 panel/node 包测试也通过（panel 9.893s，node 2.803s），记录于 `panel-tls-isolated-targeted.log`。这证明测试不依赖工作区此前未提交的 QUIC/计数修复。完整标签测试则覆盖当前工作区全部保留的改动。

必要的 Linux 编译使用 CGO_ENABLED=0、完整 release 标签、`-trimpath -buildvcs=false`；本机产物只用于编译验证，没有安装或发布。确切结果与摘要见同目录 `panel-api-tls-results.md`。

## 尚缺的证据与后续验证

- 故障进程实际加载的证书链及 SPKI 摘要，与同时间成功新进程的逐级对照；实际选中候选根的数据目前不可见。
- 生产二进制的可信来源、`go version -m` 构建信息及等价 Go 1.25.14/jsonv2/全依赖构建。用户给定的 SHA 尚未在本机核实。
- Linux 系统根子进程测试实际执行、可用 C 编译器下的 race，以及足够长的配置/证书刷新压力测试。
- 旧故障进程的内存状态无法从源码推断；诊断新构建启动后也未必能重现它。没有据此自动刷新 CA、复制/替换整个网络栈或重启服务。

流量恢复测试只证明 **TLS 握手尚未完成、HTTP 未提交** 时不发生重复上报。原生面板 push 缺少幂等报告 ID 时，服务端已计费但 ACK 丢失仍可能导致重试重复计费。此前工作区报告保留修复与 Cluster ACK 逻辑均保留；本次不宣称解决该独立边界。

## 单节点灰度与回滚计划（未执行）

1. 审批后仅选择一台节点。记录服务 unit、ExecStart、当前可执行文件真实路径、版本、SHA-256、`go version -m`、配置路径与权限、当前 CA bundle 摘要、面板 heartbeat 和用户同步时间。备份二进制、unit 与配置，私钥/token 的备份保持 root-only，禁止上传工单或日志。不要依靠重启后的“暂时正常”证明根因。
2. 在独立构建环境使用已核实生产工具链、jsonv2、CGO0、GOAMD64v1和相同 release 标签，审阅本轮补丁与生产参考的 backport 差异。不要直接把整个 HEAD 当作生产最小替换，也不要混入其他未验收修改。
3. 保持自动升级关闭。经批准后人工替换单节点二进制并执行一次计划内重启；不改面板数据、协议/认证配置、CA 信任策略或 DNS。当前任务没有执行这些操作。
4. 连续观察至少 10 个配置/用户/心跳同步周期，并进行更长时间证书轮换观察。必须同时验收：面板显示真实 heartbeat 时间持续推进；TLS 校验错误停止；用户同步得到有效响应并正确执行授权变更；现有/新建 VLESS Reality 及所用其他协议真实 HTTPS 出网成功；计费无异常跳增。仅 `systemctl is-active` 不算验收。
5. 出现 TLS 持续失败、用户同步失败、节点清空、协议连通性或计费回退时，保存脱敏诊断、时间与产物摘要，恢复备份二进制并进行一次人工回滚重启，核验原配置权限和上述验收项。不要通过关闭 TLS 校验回滚。内存待报流量可能在任何计划重启中丢失，应在维护窗口先确认积压情况；不得为了备份而复制含用户秘密的完整日志。

本轮没有连接生产、部署、替换生产二进制、重启服务、修改面板或节点配置，没有升级依赖、开启自动升级，也没有推送触发自动 Release。
