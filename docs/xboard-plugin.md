# XBoard 插件：服务器与一键部署

## 安装 / 升级

上传 [arinode-xboard-plugin.zip](../dist/arinode-xboard-plugin.zip)，在 XBoard 插件管理中安装或升级到 **0.9.2** 并启用 `ari_node`，然后刷新后台。也可以把 `integrations/xboard/AriNode` 完整复制到 `<XBoard>/plugins/AriNode` 后执行面板的插件升级流程。

## 配置自动下发

插件 0.6.0 配合新版 agent，机器模式自动下载实时节点配置、保存到受限本地文件并应用。面板保存后默认 30 秒内开始同步，只更新受影响节点；相同配置不重写、不重载。已有节点机首次执行 `sudo anctl upgrade`，之后不用因节点修改手动重启整个服务。[配置下发、文件路径与兼容范围](xboard-config-sync.md)。

## 页面内一键更新插件

第一次需手动安装 0.5.0；之后打开 `/arinode/admin` → **插件更新** → **检查更新** → **立即更新**。完成后点击“刷新页面”。检查/安装失败会显示原因；若浏览器请求中断，请重新检查当前版本后再重试。

安装源固定为官方仓库 main 分支的 `dist/arinode-xboard-plugin.zip` 和 `dist/xboard-plugin-update.json`，两者从同一个不可变 Git 提交读取。校验 SHA-256、包大小、插件标识和版本后，先备份再调用 XBoard 原生升级；同一实例同时只允许一个升级任务，不允许降级。不下载任意 URL，也不会把管理员凭证发送到 GitHub。

服务器记录、绑定和密钥保存在 XBoard 数据库中，更新不会卸载或重建。更新失败会恢复插件文件、版本、启用状态和配置。旧版保存在 `<XBoard>/plugins/.arinode-updates/<备份编号>/backup`，可按需保留或清理。包含数据库迁移的包要求手动升级，避免不可逆 DDL 导致无法回滚。

面板 PHP 需启用 cURL 和 ZIP，运行用户需能写插件父目录，网络需能访问 GitHub API 和 raw.githubusercontent.com。自动清理 Blade/路由缓存；Octane 使用平滑 reload，若无法自动重载会给出命令提示。自定义常驻 PHP 服务需要自行重载。插件更新不影响 VPS 上 AriNode 的程序版本。

## 手动更新 VPS 后端

单台更新：在 AriNode 插件的服务器列表中，点击目标服务器的 **更新后端**，查看当前版本后点击 **更新到最新正式版**。

批量更新：跨一个或多个页面勾选服务器，点击 **批量更新后端**。确认预览中的服务器范围后，点击 **下发更新任务**。点击 **全部更新后端** 会获取全部页面的机器清单，忽略当前搜索条件，并在预览中显示停用机器及其跳过状态。浏览器逐台查询状态并排队，最多同时处理 3 台，批量选择跨页面保留。

批量操作复用已有的单机状态查询及排队 API，不会创建一次性重启所有机器的后端批处理任务。停用机器、离线机器、同一机器有多个 agent 的冲突机器，以及已有 `pending` 或 `running` 任务的机器会显示为跳过并说明原因。其余机器分别创建更新任务；单台请求失败不会阻止队列继续处理其他机器。每台机器的待领取、更新中、完成或失败状态都单独展示，已经是最新版本时无需重启。

支持官方脚本安装的 Linux systemd 服务和通过 HTTPS 注册的机器模式（也支持暂未绑定节点的服务器）。全部更新获取跨所有页面的机器清单，不受当前名称搜索条件影响，并将停用机器包括在预览中以显示跳过原因。更新任务分别使用每台机器的 Redis 状态与锁，每台 VPS 应使用独立的机器记录。旧版后端需先在 VPS 执行一次 `sudo anctl upgrade`。后端需要能够访问 GitHub Releases。

安装新版会短暂重启目标 VPS 上的 AriNode，影响该进程上的所有面板绑定。若新版未通过本机健康检查，更新器会尝试恢复旧版。失败详情可在 VPS 上运行 `sudo journalctl -u arinode-panel-upgrade.service -n 80 --no-pager` 查看。手动更新与 `anctl upgrade auto` 的设置独立。

维护者修改插件后运行 `python3 scripts/package-xboard-plugin.py` 同步生成 ZIP 和校验清单；CI 会检查两者与源码一致。

无需运行数据库迁移：服务器列表复用 XBoard 的 `ServerMachine` 和节点的 `machine_id`，已有机器记录也会显示。此功能要求支持原生机器管理的新版 XBoard；后台侧栏入口还要求该后台支持 `admin_menus` / `admin_crud`。不支持侧栏扩展的后台仍可使用下面的内置页面。

## 打开页面

先登录与面板 **同域名** 的管理员后台，再打开：

```text
https://你的面板域名/arinode/admin
```

也可以从侧栏 **AriNode 服务器管理 → 管理页面** 点击打开链接，或在插件说明中点击“打开 AriNode 可视化服务器管理页”。新版后台原有的 **服务器与部署命令 / 可绑定节点** 页面继续提供兼容入口。

页面自动读取官方新版后台的 `XBOARD_ACCESS_TOKEN` 登录记录并检查有效期。旧版 / 定制后台存储方式不同，可临时填入管理员 Authorization 凭证；它仅留在当前页面内存中。普通用户或未登录访客无法读取服务器、节点或部署命令，也无法修改绑定。公开 HTML 外壳不含密钥或服务器资料。

## 原生后台表格中的部署命令

插件 0.9.1 调整 XBoard 原生 **AriNode 服务器管理 → 服务器与部署命令** 表格。命令列固定为 290px，长命令以省略形式显示，每行提供 **复制命令** 按钮。按钮复制完整命令，部署提示列固定为 140px 并允许换行。注册码过期时会提示刷新列表后重新复制。浏览器拒绝自动复制时，弹窗显示并选中完整命令，可按 Ctrl+C / ⌘C 复制。

由 Laravel 在配置的 `secure_path` 提供的管理页，会在插件启用时自动加载同源样式和脚本。升级插件后刷新管理页即可。

如果前后端分离，静态后台入口由独立 Web 服务器直接提供，则需在该入口加载以下资源，并将这两个同源路径代理到安装插件的 XBoard 后端。

```html
<link rel="stylesheet" href="/arinode/native-table.css">
<script defer src="/arinode/native-table.js"></script>
```

资源遵循后台现有 CSP，自定义策略需允许这些同源脚本和样式。也可直接使用后端提供的 `/arinode/admin` 内置页面获取部署命令。

## 添加服务器

### API 通讯地址

前后端分离部署时，在内置管理页右上角点击 **通讯设置**，填写 **API 通讯地址** 并保存。XBoard 原生插件配置中也提供相同字段。设置保存在 `v2_plugins.config` 的 `api_url`，生成新命令时立即读取。

填写节点可访问的后端 HTTPS 根地址，例如 `https://api.example.com`，支持 `https://api.example.com:8443/xboard` 这样的端口和部署子路径。请填写服务根地址，不要附加 `/api` 或 `/api/v1`。地址中不能含账号、密码、查询参数或片段。

取址优先级为显式 `api_url`、站点设置 `app_url`、当前请求域名。留空会恢复后两级取址。内置页面的部署命令、XBoard 原生插件服务器列表中的 `install_command` 和 `POST /api/v1/arinode/provision` 返回的 `ApiHost` 使用同一地址。节点用该地址兑换注册码，并持续拉取绑定和配置。

已有 VPS 更换地址时，先在插件中保存新地址并生成新命令，然后在 VPS 执行以下步骤。移除旧绑定会停止对应节点，重新注册完成前会有短暂中断。

```sh
sudo anctl panel list
sudo anctl panel remove OLD_BINDING_ID
# 执行插件使用新 API 地址生成的部署命令
```

面板地址参与本机绑定标识。已有 VPS 需要重新注册才能使用新地址，旧地址绑定应在注册前移除，以免同一机器被重复发现。其他面板的绑定可继续使用各自的地址。

### 注册机器

1. 在 AriNode 管理页点击 **添加服务器**，填写名称和备注。节点可以留空，部署后再回面板绑定。
2. 点击 **保存并生成部署命令**，选择内核和模式。默认使用 sing-box。HY1/HY2/TUIC/AnyTLS/Mieru 使用 sing-box。
3. 在目标 Linux 服务器执行复制的命令。AriNode 使用注册码向 HTTPS 面板注册，再自动拉取节点列表和配置。机器还没有绑定节点时，服务保持运行并等待下发。

生成的命令采用下面的形式（示例值不能直接部署）：

```sh
curl -fsSL 'https://raw.githubusercontent.com/Foxtea267/AriNode/main/scripts/install.sh' | sudo bash -s -- --enroll 'https://panel.example.com' 'REGISTRATION_CODE'
```

注册码有效期 30 分钟，可在有效期内重试。生成命令需要管理员身份。安装器向面板兑换机器凭据，将它写入权限为 600 的本地配置。编辑机器后使用同一地址和机器 ID 再次注册，会更新该绑定。面板地址和机器 ID 都相同的注册只保留一条记录。

服务器视图使用紧凑表格；“节点目录”可搜索协议、地址和节点归属。新增 / 编辑及部署命令使用独立弹窗，关闭命令弹窗即清空其中的密钥内容。右上角主题按钮可切换深浅色，默认跟随系统，并共享风控等插件的 `xboard_plugin_theme` 偏好。手机端表格按字段转换为纵向布局。概览中服务器数量对应当前搜索结果，最近上报时间仅统计当前页。

**多节点模式**拉取该机器的全部绑定节点。关闭后只运行节点 ID 最小的启用节点。**多面板模式**允许追加新面板，关闭时遇到其他绑定会报错。已支持注册的 AriNode 会热加载配置，首次从旧版本升级会重启服务。多个面板的节点需要使用可分别监听的端口。

已有 [V2bX](v2bx-migration.md) 或 xboard-node 可使用对应迁移流程。DNS 多 IP 节点组使用独立的成员配置。

## 在节点机管理面板

```sh
sudo anctl panel list
sudo anctl panel remove https://panel.example.com
sudo anctl panel remove <绑定ID>
```

列表显示地址、机器 ID 和节点模式。同地址存在多台逻辑机器时，用列表里的绑定 ID 精确移除。移除会从本机配置中删除对应凭据和绑定，停止它的节点，并清除 `/etc/arinode/managed-panels/<绑定ID>/` 中下载的配置和生成的证书缓存。运行中的服务由配置监听器处理卸载和清理。其他面板的文件继续保留。XBoard 的原生机器记录仍可在面板上管理。

## 编辑、状态与兼容

- 编辑绑定只更新这台机器，不能直接抢占属于另一机器的节点；先在原机器中移除该绑定再转移。
- AriNode 默认每 30 秒读取绑定变更。启用的机器即使没有节点也能生成命令。停用的机器无法兑换注册码。
- 服务器列表展示原生机器 ID、启用状态、节点 ID 和最后上报时间。最后上报时间不能替代代理协议连接测试。
- DNS 多 IP 共用一个节点仍使用 [节点组批量下发](node-cluster.md)，不通过原生单一 `machine_id` 把同一节点同时绑定到多台机器。
- 原有 JSON 下发 `POST /api/v1/arinode/provision`、节点组、在线 IP 等接口继续保留。

管理员 API：

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/arinode/admin/servers` | 分页服务器列表和默认部署命令 |
| GET | `/api/v1/arinode/admin/nodes` | 可绑定节点、安全字段、归属信息 |
| GET | `/api/v1/arinode/admin/settings` | API 通讯地址及当前生效地址 |
| POST | `/api/v1/arinode/admin/settings` | 保存 `api_url`，空字符串恢复默认取址 |
| POST | `/api/v1/arinode/admin/servers/save` | 新增 / 编辑服务器及节点绑定 |
| POST | `/api/v1/arinode/admin/command` | 按机器 ID、内核和语言生成命令 |

保存时允许 `node_ids: []`。生成命令可传 `{"id":3,"core":"sing","multi_node":true,"multi_panel":true}`。这两项均使用原生 `admin` 中间件认证。注册接口 `POST /api/v1/arinode/enroll` 接收签名注册码，并检查有效期、机器启用状态和凭据是否已轮换。

通讯设置接口使用相同的 `admin` 认证。保存可传 `{"api_url":"https://api.example.com"}`，读取会返回已保存的 `api_url` 与 `effective_url`。

## 验证

```sh
php integrations/xboard/tests/cluster.php
composer install --working-dir=integrations/xboard/tests
php integrations/xboard/tests/admin.php
node --check < integrations/xboard/AriNode/resources/views/admin-script.blade.php
```

管理页回归使用真实 Illuminate 验证器、路由、Blade 和 SQLite，覆盖创建 / 编辑、事务回滚、跨机器绑定保护、专属密钥、命令引用、协议选择、分页、停用、插件关闭和页面渲染。XBoard 模型与插件启用检查使用最小测试边界；原生 Sanctum 登录、生产 Redis 和具体定制后台需在实际面板环境验证。
