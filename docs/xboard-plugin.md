# XBoard 插件：服务器与一键部署

## 安装 / 升级

上传 [arinode-xboard-plugin.zip](../dist/arinode-xboard-plugin.zip)，在 XBoard 插件管理中安装或升级到 **0.5.0** 并启用 `ari_node`，然后刷新后台。也可以把 `integrations/xboard/AriNode` 完整复制到 `<XBoard>/plugins/AriNode` 后执行面板的插件升级流程。

## 页面内一键更新插件

第一次需手动安装 0.5.0；之后打开 `/arinode/admin` → **插件更新** → **检查更新** → **立即更新**。完成后点击“刷新页面”。检查/安装失败会显示原因；若浏览器请求中断，请重新检查当前版本后再重试。

安装源固定为官方仓库 main 分支的 `dist/arinode-xboard-plugin.zip` 和 `dist/xboard-plugin-update.json`，两者从同一个不可变 Git 提交读取。校验 SHA-256、包大小、插件标识和版本后，先备份再调用 XBoard 原生升级；同一实例同时只允许一个升级任务，不允许降级。不下载任意 URL，也不会把管理员凭证发送到 GitHub。

服务器记录、绑定和密钥保存在 XBoard 数据库中，更新不会卸载或重建。更新失败会恢复插件文件、版本、启用状态和配置。旧版保存在 `<XBoard>/plugins/.arinode-updates/<备份编号>/backup`，可按需保留或清理。包含数据库迁移的包要求手动升级，避免不可逆 DDL 导致无法回滚。

面板 PHP 需启用 cURL 和 ZIP，运行用户需能写插件父目录，网络需能访问 GitHub API 和 raw.githubusercontent.com。自动清理 Blade/路由缓存；Octane 使用平滑 reload，若无法自动重载会给出命令提示。自定义常驻 PHP 服务需要自行重载。插件更新不影响 VPS 上 AriNode 的程序版本。

维护者修改插件后运行 `python3 scripts/package-xboard-plugin.py` 同步生成 ZIP 和校验清单；CI 会检查两者与源码一致。

无需运行数据库迁移：服务器列表复用 XBoard 的 `ServerMachine` 和节点的 `machine_id`，已有机器记录也会显示。此功能要求支持原生机器管理的新版 XBoard；后台侧栏入口还要求该后台支持 `admin_menus` / `admin_crud`。不支持侧栏扩展的后台仍可使用下面的内置页面。

## 打开页面

先登录与面板 **同域名** 的管理员后台，再打开：

```text
https://你的面板域名/arinode/admin
```

也可以从侧栏 **AriNode 服务器管理 → 管理页面** 点击打开链接，或在插件说明中点击“打开 AriNode 可视化服务器管理页”。新版后台原有的 **服务器与部署命令 / 可绑定节点** 页面继续提供兼容入口。

页面自动读取官方新版后台的 `XBOARD_ACCESS_TOKEN` 登录记录并检查有效期。旧版 / 定制后台存储方式不同，可临时填入管理员 Authorization 凭证；它仅留在当前页面内存中。普通用户或未登录访客无法读取服务器、节点或部署命令，也无法修改绑定。公开 HTML 外壳不含密钥或服务器资料。

## 添加服务器

1. 先在 XBoard 节点管理中创建、启用需要部署的节点，设置协议、端口、域名和用户分组。
2. 点击 AriNode 页面右上角 **添加服务器**，在弹窗中填写名称和备注、勾选节点，点击 **保存并生成部署命令**。
3. 插件创建原生机器记录，分配专属密钥并绑定节点；编辑已有记录时保留原密钥。它不改变节点的协议、用户分组或订阅配置。
4. 默认使用 **sing-box**，也可选择 Xray。HY1/HY2/TUIC/AnyTLS/Mieru 要使用 sing-box，选择 Xray 时会明确报错。
5. 点击 **复制命令**，在目标 Linux 服务器执行。安装语言、面板地址、机器密钥、节点类型 / ID 和内核均已填好，无需再回答安装问题。

生成的命令采用下面的形式（示例值不能直接部署）：

```sh
curl -fsSL 'https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh' | sudo env ARINODE_LANGUAGE='zh' ARINODE_INSTALL_MODE='fresh' ARINODE_PANEL_URL='https://panel.example.com' ARINODE_PANEL_TOKEN='MACHINE_TOKEN' ARINODE_MACHINE_ID='3' ARINODE_NODES='vless:1 anytls:2' ARINODE_CORE='sing' bash
```

命令下载正式版并写入机器绑定配置，启动 `arinode.service`。它不会开启自动升级。命令含机器专属密钥，只向管理员返回；页面/API 使用 `no-store`，不写浏览器 URL、localStorage 或公开日志。所有命令参数都按 Linux shell 规则引用。

服务器视图使用紧凑表格；“节点目录”可搜索协议、地址和节点归属。新增 / 编辑及部署命令使用独立弹窗，关闭命令弹窗即清空其中的密钥内容。右上角主题按钮可切换深浅色，默认跟随系统，并共享风控等插件的 `xboard_plugin_theme` 偏好。手机端表格按字段转换为纵向布局。概览中服务器数量对应当前搜索结果，最近上报时间仅统计当前页。

该命令用于从零配置；目标机器已有 AriNode 配置时会先备份再替换，其他面板绑定不会自动合并。已有 [V2bX](v2bx-migration.md) 或 xboard-node 请使用对应迁移流程，不要用从零部署代替迁移。

## 编辑、状态与兼容

- 编辑绑定只更新这台机器，不能直接抢占属于另一机器的节点；先在原机器中移除该绑定再转移。
- 已部署 AriNode 的机器自动发现功能默认每 30 秒读取绑定变更。至少保留一个启用且受支持的节点才能生成部署命令。停用机器、无可用节点时不生成可执行命令。
- 服务器列表展示原生机器 ID、启用状态、节点 ID 和最后上报时间。最后上报时间不能替代代理协议连接测试。
- DNS 多 IP 共用一个节点仍使用 [节点组批量下发](node-cluster.md)，不通过原生单一 `machine_id` 把同一节点同时绑定到多台机器。
- 原有 JSON 下发 `POST /api/v1/arinode/provision`、节点组、在线 IP 等接口继续保留。

管理员 API：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/arinode/admin/servers` | 分页服务器列表和默认部署命令 |
| GET | `/api/v1/arinode/admin/nodes` | 可绑定节点、安全字段、归属信息 |
| POST | `/api/v1/arinode/admin/servers/save` | 新增 / 编辑服务器及节点绑定 |
| POST | `/api/v1/arinode/admin/command` | 按机器 ID、内核和语言生成命令 |

保存请求：`{"name":"US-01","notes":"192.0.2.1","node_ids":[1,2],"is_active":true}`；编辑时增加 `"id":3`。生成命令：`{"id":3,"core":"sing","language":"zh"}`。全部使用 XBoard 原生 `admin` 中间件认证，返回 JSON 不缓存。

## 验证

```sh
php integrations/xboard/tests/cluster.php
composer install --working-dir=integrations/xboard/tests
php integrations/xboard/tests/admin.php
node --check < integrations/xboard/AriNode/resources/views/admin-script.blade.php
```

管理页回归使用真实 Illuminate 验证器、路由、Blade 和 SQLite，覆盖创建 / 编辑、事务回滚、跨机器绑定保护、专属密钥、命令引用、协议选择、分页、停用、插件关闭和页面渲染。XBoard 模型与插件启用检查使用最小测试边界；原生 Sanctum 登录、生产 Redis 和具体定制后台需在实际面板环境验证。
