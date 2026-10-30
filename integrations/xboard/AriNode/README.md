# AriNode 服务器管理

插件版本 **0.6.0**。安装 / 升级后启用插件，并刷新 XBoard 管理员后台。

## 节点配置自动下发

配合支持此接口的新版 AriNode，机器模式每个同步周期（默认 30 秒）直接获取最新节点配置。修改端口、传输、证书、路由或 multiplex 后自动应用，只更新受影响的节点，无需手动重启整个服务。配置文件保存到 `/etc/arinode/panel-config/<绑定摘要>.json`，权限 600；相同配置不重写，不触发重载。错误配置不覆盖上一份已生效文件，并自动重试。新增/移除绑定继续使用机器自动发现。

已有节点机首次需要 `sudo anctl upgrade`；只更新 PHP 插件不能增加旧 agent 的协议能力。没有新版插件时继续使用 XBoard 原生接口。多面板、Komari 和本地内核配置不会被远程节点文件覆盖；非 root/容器可通过节点 Options 的 `PanelConfigPath` 指定可写持久化目录。详情见[自动下发说明](https://github.com/Foxtea267/AriNode/blob/main/docs/xboard-config-sync.md)。

### 页面内更新

首次升级到 0.5.0 仍需上传本插件 ZIP。此后在 `/arinode/admin` 点击“插件更新”，检查版本后点击“立即更新”，最后刷新页面即可。

更新源固定为 `Foxtea267/AriNode` 的 main 分支发布包。后端将更新清单与 ZIP 固定到同一 Git 提交，校验版本、包身份、大小及 SHA-256，再调用 XBoard 原生 `PluginManager::update`。不接受自定义下载地址，不自动降级，不定时自动安装。仅管理员可检查和安装，浏览器凭证不会发给 GitHub。

更新会保留 XBoard 中的服务器、节点绑定、机器密钥及插件配置。旧文件保存在 `<XBoard>/plugins/.arinode-updates/<备份编号>/backup`；失败时自动恢复文件与插件数据库状态，不卸载插件。不支持包含数据库迁移的更新包，这类版本需走 XBoard 手动升级流程。备份需由管理员按需要清理。

要求 PHP cURL / ZIP 扩展、面板能访问 GitHub API 与 raw.githubusercontent.com，且 PHP 运行用户可写插件父目录。在线更新同时清理视图和路由缓存；检测到 Octane 时尝试平滑重载工作进程，失败会明确提示执行 `php artisan octane:reload`。其他自定义常驻 PHP 容器需自行重载。此功能不更新各 VPS 上的 AriNode / anctl。

## 内置页面

[打开 AriNode 可视化服务器管理页](/arinode/admin)

请在同一域名的 XBoard 后台登录，再打开上面的页面。它自动使用新版后台的管理员登录状态；旧版后台可在页面中临时填写管理员 Authorization 凭证。

1. 点击右上角“添加服务器”，在弹窗中填写名称和备注（可写 IP / 域名），勾选已有节点。
2. 点击“保存并生成部署命令”。插件创建 XBoard 原生机器记录、生成专属密钥并绑定节点。
3. 点击“复制命令”，在目标 Linux 服务器执行。默认 sing-box，可改为 Xray；不需要再填写面板地址、密钥或节点 ID。
4. 在服务器列表查看机器 ID、绑定节点和最后上报时间。需要变更时点击“编辑绑定”。

页面采用与风控插件一致的紧凑黑白风格，提供服务器表格、节点目录、编辑和部署弹窗、手机布局，以及深浅色主题切换。主题偏好与其他使用 `xboard_plugin_theme` 的插件共享。

支持插件菜单的 XBoard 后台还会增加 **AriNode 服务器管理 → 管理页面** 入口，点击链接打开完整页面。原生 **服务器与部署命令 / 可绑定节点** 页面继续提供兼容入口。

节点协议、端口、订阅分组等仍在 XBoard 的节点管理中配置。其他服务器占用的节点不能直接抢占，需要先从原服务器解绑。XBoard 原生机器记录会显示在此页面；原有机器密钥不会因编辑而重置。

命令包含机器专属密钥，仅管理员接口返回。不要公开分享命令。页面不会通过 SSH 连接或自动执行命令。部署命令用于从零配置；已有 AriNode 配置会备份后替换，不会自动合并其他面板绑定。

已有 V2bX / xboard-node 请使用仓库文档中的迁移命令，保留原配置和迁回能力。DNS 多 IP 节点组仍使用原来的批量成员下发接口。

## 原有接口

- `POST /api/v1/arinode/provision`：为已有节点生成 AriNode JSON。
- `GET /api/v1/arinode/node/config`：使用原生 machine token 鉴权的实时节点配置文件。
- `/api/v1/arinode/cluster/*`：节点组成员与聚合上报。
- `/api/v1/arinode/devices/alivelist`：原生节点在线 IP 信息。

[安装与页面说明](https://github.com/Foxtea267/AriNode/blob/main/docs/xboard-plugin.md) · [节点组说明](https://github.com/Foxtea267/AriNode/blob/main/docs/node-cluster.md)
