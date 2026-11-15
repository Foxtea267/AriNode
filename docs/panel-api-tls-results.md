# 面板 TLS 隔离验证记录

源码基准：`9a6d68ee523e060d5e72c4446933e67d3e592f65`。运行平台为 Windows amd64、Go **1.25.0**，不是用户的 Linux Go1.25.14 生产环境。以下结果均来自本地回环，不包含生产验收。

统一环境：

```powershell
$env:GOEXPERIMENT='jsonv2'
$env:GOMAXPROCS='2'
$env:GOMODCACHE="$PWD/.tools/mod"
$env:GOCACHE="$PWD/.tools/cache"
$env:GOTOOLCHAIN='local'
```

实际 Go 可执行文件为 `.tools/go-dsom/go/bin/go.exe`。表中简写 `go` 指此文件。

| 检查 | 实际结果 | 完整输出 |
| --- | --- | --- |
| recovery 测试，状态修复前 | FAIL：`TLS failure cleared the last valid alive cache` | `.tools/panel-tls-recovery-before.log` |
| recovery 测试，状态修复后 | PASS，3.587s | `.tools/panel-tls-recovery-after.log` |
| `go test -p 2 -v ./api/panel ./node -count=1 -timeout=240s`，交付前 | PASS：panel 8.775s、node 3.802s | `.tools/panel-tls-targeted-delivery.log` |
| 独立 TLS 补丁应用到干净 HEAD 归档后 `go test -p 2 ./api/panel ./node -count=1 -timeout=240s` | PASS：panel 9.893s、node 2.803s；不依赖工作区此前的 QUIC/计数修复 | `.tools/panel-tls-isolated-targeted.log` |
| 完整 release 标签 `go test -p 2 ... ./... -count=1 -timeout=240s`，交付前 | 所有包 PASS；panel 9.706s、sing 30.845s、node 3.677s | `.tools/panel-tls-full-delivery.log` |
| `CGO_ENABLED=1 go test -race -p 2 ./api/panel ./node -count=1 -timeout=240s` | 未运行：编译失败，`gcc` 不存在 | `.tools/panel-tls-race-final.log` |
| Linux amd64 全标签测试二进制 `go test -c ./api/panel` | 编译退出 0；Linux-only 默认系统根/依赖初始化测试未执行 | `.tools/panel-tls-compile-linux.log` |
| Linux amd64/arm64 服务程序全标签编译 | 编译退出 0；CGO0，无部署 | `.tools/panel-tls-build-delivery.log` |
| 原源码归档 trojan-httpupgrade 对照 `-count=10` | 8 次 EOF 失败、2 次 PASS | `.tools/panel-tls-trojan-baseline.log` |
| 当前源码 trojan-httpupgrade 单项重复 `-count=5` | 1 次 EOF 失败、4 次 PASS | `.tools/panel-tls-trojan-repeat.log` |

完整标签与命令：

```sh
GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 \
  -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' \
  ./... -count=1 -timeout=240s

GOEXPERIMENT=jsonv2 GOMAXPROCS=2 go test -p 2 -v ./api/panel \
  -run '^TestPanelTLSRecoveryPreservesSnapshotsAndReportsOnce$' -count=1 -timeout=120s

# 编译验证，不执行 Linux 产物：
GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 GOEXPERIMENT=jsonv2 \
go test -p 2 -c -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' \
  -o .tools/panel-tls-delivery-linux.test ./api/panel

# GOARCH 分别为 amd64、arm64；GOAMD64=v1 仅影响 amd64。
GOOS=linux GOARCH=amd64 GOAMD64=v1 CGO_ENABLED=0 GOEXPERIMENT=jsonv2 \
go build -p 2 -trimpath -buildvcs=false \
  -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' \
  -o .tools/panel-tls-delivery-amd64 .
```

已有失败现场保持原样。未删除关键断言，也没有把最终一次通过描述为不存在协议间歇失败。

Linux-only 系统根测试与 `-race ./api/panel ./node` 已纳入源码检查门禁，尚未执行远程 CI，不存在 CI 通过结论。测试关闭的是本地 fixture/idle 连接，用于强制证书轮换后的新握手；生产连接复用策略未改变。

独立 TLS 补丁导出到 `.tools/panel-api-tls.patch`，排除此前工作区的 QUIC、计数、待报流量修复；原有改动保留在工作区。补丁针对当前 HEAD，不是可以未经审阅直接应用到生产参考 commit 的 backport。

补丁包含的文件：

| 用途 | 文件 |
| --- | --- |
| Resty 安全诊断与错误脱敏 | `api/panel/panel.go`、`utils.go`、`config.go`、`machine.go`、`node.go` |
| 用户同步脱敏与设备快照发布 | `api/panel/user.go` |
| 调用方日志地址脱敏 | `node/controller.go`（仅 buildNodeTag 一处）、`node/discovery.go`、`node/node.go`、`cmd/backend_update_linux.go` |
| 面板回归测试 | `api/panel/devices_test.go`、`error_sanitization_test.go`、`tls_chain_test.go`、`tls_recovery_test.go`、`tls_dependencies_linux_test.go` |
| 管理器回归测试 | `node/panel_tls_discovery_test.go` |
| CI | `.github/workflows/release.yml`（仅额外 TLS race gate） |
| 报告 | `docs/panel-api-tls.md`、`docs/panel-api-tls-results.md` |

导出工具 `.tools/export-panel-tls.py` 已在干净归档中执行 `git apply --check`、应用补丁，并逐文件核对预期内容；没有改动主工作区的 Git index。没有把先前的 `node/controller.go` 上报锁字段或 CI 其他 race 任务混入独立补丁。

缺口仍包括生产 ECDSA 根因复现、等价 Go1.25.14 构建、实际 Linux/race 运行和长时间观测。未连接生产、未部署或重启、未修改面板/节点数据、未升级依赖、未开启自动升级、未推送发布。
