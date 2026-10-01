# 协议验证范围

默认 `sing` 内核的测试通过实际认证连接、TCP 回显、UDP 回显及用户流量计数验证代理。测试启用与正式安装包相同的功能标签，并在 GitHub Actions 的 Linux 环境运行。

| 协议 | TCP 转发 | UDP 转发 | 额外验证 |
| --- | --- | --- | --- |
| VMess | 是 | 是 | TCP、WebSocket |
| VLESS | 是 | 是 | TCP、WebSocket、gRPC、HTTPUpgrade、HTTP/h2 |
| Trojan | 是 | 是 | TLS、WebSocket、gRPC、HTTP/h2 |
| Shadowsocks | 是 | 是 | AES-128-GCM、AES-256-GCM、ChaCha20-IETF-Poly1305 |
| Shadowsocks 2022 | 是 | 是 | BLAKE3-AES-128-GCM、BLAKE3-AES-256-GCM，多用户密码格式 |
| Hysteria 1 | 是 | 是 | QUIC；面板须提供正数上下行带宽 |
| Hysteria 2 | 是 | 是 | QUIC；兼容原生 `hysteria`、`version: 2`，零带宽自动模式 |
| TUIC v5 | 是 | 是 | QUIC、UUID/密码、ALPN `h3` |
| AnyTLS | 是 | 是 | TLS、UDP over TCP |
| Mieru | 是 | 是 | 官方 v3.29.0 客户端，TCP/UDP 底层传输、traffic pattern、撤销用户 |
| VLESS XHTTP | 是 | 未在该用例中测试 | 默认绑定自动选择 Xray，真实认证回显 |

测试文件：`core/sing/protocols_integration_test.go`、`core/sing/mieru_test.go`、`core/xhttp_integration_test.go`。面板原生协议字段和机器增删/故障恢复另有 API 与节点管理测试。完整测试命令：

```sh
GOEXPERIMENT=jsonv2 go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./...
```

正式版 `20261001-0ae73543` 在真实测试机上使用面板实际用户完成以下外网验证：

| 协议 | HTTPS / 出口 IP | UDP DNS |
| --- | --- | --- |
| Mieru（TCP 底层传输） | 200 / 测试机 IP | 通过 |
| Hysteria 2 | 200 / 测试机 IP | 通过 |
| Shadowsocks 2022 AES-128 | 200 / 测试机 IP | 通过 |
| TUIC v5 | 200 / 测试机 IP | 通过 |
| VLESS XHTTP + Reality | 200，使用修正后的客户端 extra | 未在该外网用例中测试 |

XHTTP 面板原始 extra 中的无效 `downloadSettings`/空对象仍会导致客户端在连接前解析失败，需要管理员在面板中修正；服务器节点 token 无权修改订阅。其他协议的验证为本地真实连接测试，未在该测试机上建立对应面板节点。测试不表示任意客户端、传输组合、证书设置或防火墙配置均已覆盖。

新机器绑定自动发现后仍会执行各节点独立的配置与认证检查。无效证书、协议版本或端口冲突会让对应节点进入重试状态，不停止健康节点。TUIC 目前支持 v5；HY1 需要显式正数带宽。使用校验 TLS 的客户端需要受信任证书或配置证书信任，`auto` 自签模式只用于面板明确允许跳过校验的节点。
