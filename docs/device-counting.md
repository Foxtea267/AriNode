# 设备数与离线清理

XBoard / AriNode 的设备限制按**每个用户的来源 IP 并集**计算，不是 TCP 连接数，也不能识别物理设备。一台设备的 IPv4 与原生 IPv6、蜂窝网络与 Wi-Fi 地址可能分别占一个名额；不能为了让数字变小，直接把不相关的 IP 合并或把显示值截断到套餐上限。

## 修复的后端问题

普通节点与 machine 模式原先只在本轮存在在线 IP 时调用 `/alive`。用户离线后，旧 UID 被省略，或者整次上报被跳过。检查到的 XBoard `ServerService::processAlive` 只替换本次请求中出现的 UID，`DeviceStateService::setDevices` 才会同步用户的 `online_count`；因此省略 UID 不等于明确清空。即使 Redis 中的 IP 之后过期，数据库里的后台显示值也可能仍是最后一次写入的数字。

`node/user.go` 现在记住本实例尝试上报过的 UID。用户不再在线时发送 `{"用户ID":[]}`，清除成功才停止重试；不会清理其他节点的 IP。网络断开或确认包丢失后也保留待清除集合。未修改计费流量格式、套餐设备上限或面板数据库结构。

为避免清空逻辑误伤一直在下载、但没有新握手的用户，`limiter/devices.go` 跟踪实际连接生命周期。sing-box 的 TCP/UDP 包装器和 Xray 的上下行 link 负责登记和释放引用。同用户同 IP 的多连接只计一次；IPv4-mapped IPv6 归一化；来源端口不计入设备身份。账号撤销会剔除该账号的活跃引用，旧连接结束不能删除重新授权后的新连接。

默认 sing-box 下的 HY2/TUIC/AnyTLS 复用上述跟踪。独立 `hysteria2` core 的原生 EventLogger 没有稳定的连接 ID，换网后不能可靠地用地址对应原连接，因此保留既有的请求活跃统计，没有添加可能泄漏的 QUIC 会话引用。只有用户级总流量而没有新源 IP 时，不凭空续期旧 IP，也不将仍有流量的授权用户直接声明离线；流量结束后仍会发送清空。该独立内核的长时间单请求流不能据此保证精确在线统计；需要此能力时使用默认 sing-box 内核。

另外修复了两个独立问题：

- Cluster 原先用用户总流量刷新该用户全部历史 IP。已断开的七个旧 IP 加一个当前 IP，可以在本地回归中复现错误地保持八个。现在只刷新本轮观测到或实际连接中的 IP，旧地址按原有窗口过期。这不是未启用 Cluster 的用户后台跳数的直接证据。
- 最近一个统计周期的设备缓存原先只按 IP 保存 UID，两个账号共用一个 NAT 地址会相互覆盖。现在使用 UID + IP 作为缓存身份。

## 上线与排查

升级包含修复的 AriNode 后，离线清理在接下来的上报周期生效。TCP 半关闭、QUIC 网络失联等情况仍要等待内核确认断开或其超时；Cluster 还保留既有的短期活跃窗口。日志 `Report online devices` 的 `online_users`、`device_ips`、`cleared_users` 分别表示本次报告的在线用户数、来源 IP 数和明确清空的用户数，不输出 UUID 或具体用户 IP。

确认后台的数字是否异常时，应对比同一时刻 Redis 尚未过期的来源 IP、后台数据库 `online_count`、节点日志中的清空报告，以及其他仍在运行的节点。没有具体用户和时间的线上快照，不能仅凭“8”认定账号共享或断定某一个原因。

修复不会批量重置所有用户的 `online_count`。进程重启会丢失内存中尚未确认的在线报告及清除集合；如果数字是升级前已停机节点留下的数据库旧值，需要在面板侧根据有效在线记录重新汇总，不能靠当前节点随意删除其他节点的数据。不建议全量清 Redis 或把所有用户设备数直接改为零。

## 回归测试

- 普通节点与 machine 接口：先上报八个 IP，再离线，明确发送空数组；清除失败会重试。
- 长连接跨多次报告保持在线，最后一个连接关闭后退出统计。
- 多连接、UDP、IPv4-mapped IPv6 去重，NAT 下的不同用户互不覆盖。
- 账号撤销、重新授权、并发登记/关闭/快照，以及 Cluster 旧 IP 不被用户流量续期。
- 真正的 Xray / sing-box VLESS 代理连接，验证多个来源端口只计一个 IP，以及真实连接关闭后的释放。

```sh
GOEXPERIMENT=jsonv2 go test -p 2 ./...
GOEXPERIMENT=jsonv2 go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./...
GOEXPERIMENT=jsonv2 go test -race ./limiter
```
