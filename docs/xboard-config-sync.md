# XBoard 节点配置自动下发

## 使用

1. 在 AriNode 插件页面更新到 **0.6.0** 并启用插件。
2. 节点机首次执行 `sudo anctl upgrade`，安装包含配置同步功能的新版 agent。
3. 保持机器模式（`MachineID > 0`）和默认的自动发现。以后在面板修改节点并保存，默认 30 秒内开始同步；实际时间还取决于网络和配置应用耗时。

端口、传输、TLS/证书、路由、outbounds 和 multiplex 均走原有强类型解析、校验及内核编译流程。协议类型、绑定新增/移除通过机器发现同步。无需再次运行部署命令，也无需手动重启整个 AriNode 服务。已有服务首次升级会由升级器重启一次。

## 配置文件和应用过程

```text
XBoard 保存节点
  -> 插件从数据库实时构建节点配置
  -> AriNode 每个同步周期下载
  -> 规范化比较、校验
  -> 仅热更新相应节点的路由或重建该节点 inbound
  -> 原子保存已生效配置文件
```

插件接口为 `GET /api/v1/arinode/node/config`，复用 `server.v2` 验证机器密钥和节点所属关系，重新读取节点记录，再调用 XBoard 原生 `ServerService::buildNodeConfig`。补齐部分 XBoard 版本漏出的 structured custom route rules。响应带 `X-AriNode-Config: 1` 和 SHA-256 版本摘要，禁止代理缓存。每次构建新配置，避免旧 ETag 令 agent 一直收到 304。

成功应用后文件位于 `/etc/arinode/panel-config/<面板+机器+节点摘要>.json`，内容是插件下发的 native panel JSON，**不是可直接传给内核的配置**。目录 700、文件 600，包含 TLS 私钥时也按相同权限保护，不记录配置正文或机器密钥。相同有效配置不重写、不改变 mtime、不重载。不同节点/面板各有文件，不互相覆盖。

本地 `/etc/arinode/config.json` 保留为启动配置，管理面板地址、凭据、本地内核参数与 Komari。远程节点文件不会覆盖它；也不会改变本地 `OutboundConfigPath`、`RouteConfigPath`、`OriginalPath` 等兼容选项。节点 Options 可设置 `PanelConfigPath`，供容器或非 root 用户指定可写的持久化目录。

插件文件写入失败时节点仍保持运行，日志明确提示保存失败，后续同步重试。非法路由、证书或内核配置会保留上一份可用节点和快照，不把失败版本标记为已生效。用户撤权仍优先处理；用户接口临时失败不会再阻断配置下载，但撤权应用失败时必须先重试撤权，防止恢复旧凭据。

## 兼容范围

- 新 agent + 插件 0.6.0：自动采用实时配置文件接口，普通同步无需额外设置。
- 新 agent + 旧插件/未装插件：回退原生 XBoard API。缺少插件的能力探测间隔为五分钟，因此后装插件最长五分钟被发现。
- 旧 agent + 新插件：旧接口继续保留，需升级 agent 才能使用新的文件下发通道。
- `MachineID=0` 的传统节点保持原生 API 行为；不自动转为机器认证。
- `MachineAutoDiscover=false` 仍可同步显式绑定节点的配置，但不自动加入/移除绑定。
- 路由热更新沿用内核支持范围；端口/TLS/transport/mux 变化可能短暂重建对应 inbound，同实例其他节点继续运行。
- 更换内核类型等全局 core 参数仍属于本地配置操作，不由面板节点文件远程修改。

## 核验

```bash
journalctl -u arinode --since '5 minutes ago' --no-pager | \
  grep -E 'Panel configuration synchronized and applied|Node reloaded|Panel routing updated|snapshot could not be saved'
```

修改一个节点的端口或证书后，应出现该节点一次应用日志；同一配置重复拉取不应再次出现重载日志。日志包含节点 ID、配置摘要和本地文件路径，不包含 UUID、token 或私钥。

自动测试覆盖：插件鉴权路由、绑定变更拒绝、数据库新值即时下发、稳定版本摘要、旧 ETag 不阻断更新、旧面板回退、重复配置不重写、单节点更新/失败恢复、用户接口失败时继续同步，以及原有撤权回归测试。
