# XBoard 面板证书与 AnyTLS / HY2 / TUIC 重载修复

## 已确认的根因

旧版 `api/panel/node.go` 的 CommonNode 没有 `cert_config`，面板证书在反序列化时被丢弃。`node/cert.go` 只处理本地 CertConfig，因此没有 content 落盘过程。`core/sing/node.go` auto 分支调用 `common/cert.SelfSigned()`，每次 AddNode 都生成新证书；AnyTLS、HY1、HY2、TUIC、Trojan 等 TLS inbound 共用此逻辑。Xray 和独立 HY2 内核也有 auto 分支。

2026-10-24 对报告中的公网端口做了两次只读握手，SNI 为 `jp1.standard.novonet.node.554733.xyz`，两次 issuer / subject 均为 `CN=jp1.standard.novonet.node.554733.xyz`。以下为真实修复前基线（UTC），不是生产已升级的证明：

| 采样时间 | notBefore | SHA-256 |
| --- | --- | --- |
| 11:22:26 | 11:17:23 | `a215f53badf1d23de317cf54383ba97d8afbdf469c1d8b593e7bfadc7311cefb` |
| 11:23:06 | 11:17:53 | `1e8d02493723c78746f51d2bd66929871f67097c0c31e555565039883613c4a3` |

旧 GetNodeInfo 按整个 HTTP 响应原始字节计算 hash，空白、对象顺序、未使用的时间戳会被当成更新。在支持热路由更新的当前内核上，这未必必然重启 inbound，应结合版本、连续 API 响应指纹及日志定位。指定 XBoard `4f48e61` 的构建函数本身没有每次随机生成 padding 或附加时间戳，不能把这些猜测当成已证实根因；本次实机确认的循环原因见下文。

### 后续实机定位：失败重试与恢复造成重签

2026-10-24 获得节点机访问后，确认其旧版本是 `20261012-302e5629`。节点 244 的连续两次 config 响应相隔 35 秒，**逐字节相同**，SHA-256 都为 `ca3825c8d9af74bfaf320d59d6bfdec47a212674114953693fb6d900cb252c01`。实际响应包含有效 content 证书，`allow_insecure=false`，SNI 此时已经为空。

该版本 `api/panel/node.go` 的 CommonNode 不接收 cert_config，`core/sing/node.go:147` 因而拒绝新节点的自动自签。旧 `node/task.go` 在删除旧 inbound 后才尝试 AddNode，失败则用旧 NodeInfo 再次 AddNode；旧信息允许自签，于是 `core/sing/node.go:151` 每次恢复都重新签发。待更新的配置继续重试，形成以下真实日志（UTC）：

```text
12:10:04 level=error msg="Node reload failed; will retry" error="add new node: TLS requires CertConfig (file, http, dns or self); panel does not allow an automatic self-signed certificate" node=anytls:244
12:10:35 level=error msg="Node reload failed; will retry" error="add new node: TLS requires CertConfig (file, http, dns or self); panel does not allow an automatic self-signed certificate" node=anytls:244
12:11:05 level=error msg="Node reload failed; will retry" error="add new node: TLS requires CertConfig (file, http, dns or self); panel does not allow an automatic self-signed certificate" node=anytls:244
```

上面只缩短了节点运行标识，没有打印 API 密钥或用户标识。此次循环的直接原因已经确认是**证书被忽略后，新配置加载失败、旧配置恢复重签**；不是 API 随机字段或 systemd 不断重启。修复先准备/校验证书，再移除旧 inbound，并在失败恢复时复用旧 runtime certificate paths。

## Agent 行为

- 统一接收 cert_config，兼容 cert_mode / mode，以及 content、file、self、http、dns。面板明确提供有效配置时优先于本地；没有新字段时保留本地选择。
- content 校验完整 PEM 链格式、私钥配对及叶证书有效期，写入 `/etc/arinode/certs/<绑定命名空间>/<内容摘要>/cert.pem` 和 `key.pem`。目录 700，文件 600。
- 命名空间含面板、machine、协议、节点 ID。相同内容复用路径，不改内容和 mtime；新证书/私钥在临时目录写完后一次 rename 发布整个目录。旧代保留供失败恢复，不覆盖活动证书。
- 本地节点 Options 可设置 `CertStorePath`，供非 root 或容器指定可写、持久化目录。
- 无面板/本地证书且 allow_insecure=true 时，auto 自签也持久化，重建 inbound 或重启程序不重签；更改 SNI 或缓存过期后重建。allow_insecure=false 时仍不自动生成不可信证书。
- 显式 content 配置损坏、缺字段或不配对时，更新报错并保留工作中的节点，不静默回退自签。撤权同步仍先于配置更新执行。
- 按规范化后的已解码运行配置比较，稳定 map 顺序、PEM 换行及 mode 别名，忽略未知元数据；保留真正影响协议的 padding、路由、multiplex、TLS 变化。
- 在原有节点生命周期中传递各内核已有的 file 模式参数，只更新对应节点，保留共享 core、多绑定、用户同步和统计架构。

日志格式（占位符不是生产数据）：

```text
level=info msg="TLS certificate active" node_id=244 cert_source=panel cert_fingerprint=<叶证书SHA256> cert_path=<缓存路径>/cert.pem key_path=<缓存路径>/key.pem
level=info msg="Node reloaded with ... users" tag=<节点运行标识>
```

debug 级别下，每次 200 响应还有 response_hash、config_hash、changed，便于确认“原始响应变化但有效配置不变”；304 保留原缓存逻辑。不记录 PEM、私钥、用户 UUID、token 或原始 API 响应。

## XBoard 配套最小补丁

[4f48e61-tls-certificates.patch](../integrations/xboard/patches/4f48e61-tls-certificates.patch) 固定针对 `4f48e61a2cbc6db5338872b6bdb45ef954ec1256`，只修改：

1. ServerService：关联数组稳定排序，路由/padding 列表顺序保留。
2. ServerSave：content 保存前校验证书配对、有效期和 SNI/SAN，不匹配返回明确验证错误并阻止保存；只编辑 SNI 也检查已有证书。
3. General::buildAnyTLS：分享链接加入 security=tls。
4. 新增小工具类 App\Support\NodeTlsConfig。

不修改 MySQL 表、节点 ID、machine 记录或密钥，也不自动修改现有 SNI。

**第二个问题要另外纠正：** `*.554733.xyz` 覆盖 `jp1.554733.xyz`，不覆盖 `jp1.standard.novonet.node.554733.xyz`。把面板 SNI 改为证书覆盖的名字，或更换含原 SNI 的证书。拨号地址仍可为 `2021.luneza.cc:25331`；客户端需要刷新订阅，不应跳过验证。

先备份源码文件、确认容器名称，再在 `/www` 应用：

```bash
docker cp integrations/xboard/patches/4f48e61-tls-certificates.patch YOUR_XBOARD_CONTAINER:/tmp/arinode-tls.patch
docker exec -w /www YOUR_XBOARD_CONTAINER git apply --check /tmp/arinode-tls.patch
docker exec -w /www YOUR_XBOARD_CONTAINER git apply /tmp/arinode-tls.patch
docker exec -w /www YOUR_XBOARD_CONTAINER php artisan optimize:clear
docker restart YOUR_XBOARD_CONTAINER
```

容器需要 git；也可在同版本源码上应用后构建派生镜像。容器内临时修改会在重建后丢失，应将补丁纳入派生镜像或源码部署；其他提交先 git apply --check，不要强行覆盖。

包含本修复的正式版本发布后，通过 `sudo anctl upgrade` 更新 agent。**插件页面中的“插件更新”只更新 PHP 插件，不更新 VPS 的 AriNode，也不应用此 XBoard 核心补丁。**

## 验证与测试边界

自动测试覆盖：证书解码、元数据变化、换行/别名、600 权限、mtime 不变、错误私钥、轮换仅一次、machine 模板不变、失败恢复旧路径、自签复用、本地 self/file 兼容。

真实 sing-box 内核集成测试针对 AnyTLS/HY2/TUIC：客户端开启校验、信任本地测试 CA，加载完整四证书链，认证后转发 HTTP，换证后重新连接。测试 CA 不等同于生产 Let's Encrypt；生产仍需部署后验收。

### 已部署版本与生产验收

节点已从 `20261012-302e5629` 升级至正式版 [`20261024-28223ff8`](https://github.com/Foxtea267/AriNode/releases/tag/20261024-28223ff8)，升级前保留了旧程序和配置的受限备份。没有改动面板数据库或节点绑定。

- `12:12:34 UTC`，节点 244 日志显示 `TLS certificate active cert_source=panel`，叶证书 SHA-256 为 `a1eb6d38eb41ac477f7afa96203aa98057e1de09e79e0fda4a668957a8daf6b2`，issuer 是 Let's Encrypt YE1。
- 本机 OpenSSL 严格验证证书链和 `jp1.554733.xyz`，返回 `Verification: OK` / `Verify return code: 0 (ok)`。
- 公网严格 TLS 采样运行五分钟（UTC 12:12:48 至 12:17:47，11 次握手），全部校验成功，SHA-256 完全相同；服务端实际发送四张证书。
- 实际 AnyTLS 客户端通过公网 `2021.luneza.cc:25331` 认证；TLS `insecure=false`，HTTPS IP 查询成功，出口为 `142.91.108.202`。用户凭据只在内存中传递，没有打印或写入测试配置文件。
- content 和 self 模式缓存的证书/私钥均为 600，复查时内容和 mtime 均未改变；节点 244、259、260 启动后各仅记录一次证书加载，reload_success=0、reload_failure=0，进程 PID 保持不变。
- Linux CI 的默认/完整内核测试及 XBoard PHP 补丁测试通过。Windows 完整内核测试曾在 `trojan-httpupgrade` 出现间歇 EOF，单项重跑通过；未删除或放宽测试。

仍需区分两个面板问题：244 当前 SNI 为空，应设置为证书覆盖的具体名称并刷新订阅；261 与 244 使用相同的 TCP 25331，启动失败日志明确为 address already in use。261 的失败被隔离，不会撤销 244 的监听。261 使用的 Cloudflare Origin CA 证书也不是普通客户端默认信任的公共 CA 证书。

上述实机访问是节点机权限，**尚未在面板主机应用 PHP 核心补丁**；分享链接的 `security=tls` 和保存时 SAN 校验仍需部署该补丁。生产换证的“一周期只 reload 一次”也尚未通过修改真实面板证书验收；已有完整内核自动测试覆盖，不能把自动测试写成生产已完成的换证试验。

```bash
openssl s_client -connect 2021.luneza.cc:25331 \
  -servername jp1.554733.xyz -verify_hostname jp1.554733.xyz -verify_return_error </dev/null

# 严格校验证书链和名称，每 30 秒采样，持续 5 分钟。
python3 scripts/check-tls-certificate.py --host 2021.luneza.cc --port 25331 \
  --server-name jp1.554733.xyz --duration 300 --interval 30

journalctl -u arinode --since '10 minutes ago' --no-pager |
  grep -E 'TLS certificate active|Node reloaded|Node reload failed|configuration fingerprints'
```

jp1.554733.xyz 是示例 SAN 内名称，须核对实际证书。TCP TLS 采样不能用于 HY2/TUIC 的 QUIC 端口，后两者用开启校验的对应客户端验收。配置不变应没有新的 Node reloaded；换一次证书只出现一次对应节点 reload。实际客户端还需测试访问目标网站。

```bash
python3 scripts/test-xboard-tls-patch.py
GOEXPERIMENT=jsonv2 go test -p 2 -tags 'sing xray hysteria2 with_quic with_grpc with_utls with_wireguard with_acme with_gvisor' ./...
```

PHP 测试下载固定提交的三个上游文件，在临时目录实际应用补丁，测试真实构建函数和保存回调；不访问生产 DB。

## 兼容性与回滚

- 本地 file/self 继续使用原路径，不重签；本地 http/dns 保留续期逻辑。没有面板证书时不强制迁移本地文件。
- 原先内存中临时生成的 auto 自签首次升级会变为持久化证书，指纹改变一次；之后复用。固定旧指纹的客户端需要更新。
- 缓存含私钥，需持久化并限制访问；旧代不自动清理，确认无活动引用后由管理员清理。
- 更新前将 `/usr/local/bin/arinode`、`/usr/local/bin/anctl` 和配置备份到受限目录。回滚时停止服务、恢复两个旧程序和原配置再启动；升级器成功后会清理临时 staging，不可将其当永久备份。
- 面板回滚用 git apply -R --check，再 git apply -R，清缓存并重启；没有数据库迁移。
- 旧 agent 仍忽略面板证书；必须回滚时，可使用其已有本地 CertConfig file 模式指向已落盘证书临时兼容。
