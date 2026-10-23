# AriNode 服务器管理

插件版本 **0.4.0**。安装 / 升级后启用插件，并刷新 XBoard 管理员后台。

## 内置页面

[打开 AriNode 可视化服务器管理页](/arinode/admin)

请在同一域名的 XBoard 后台登录，再打开上面的页面。它自动使用新版后台的管理员登录状态；旧版后台可在页面中临时填写管理员 Authorization 凭证。

1. 填写服务器名称和备注（可写 IP / 域名），勾选已有节点。
2. 点击“保存并生成部署命令”。插件创建 XBoard 原生机器记录、生成专属密钥并绑定节点。
3. 点击“复制命令”，在目标 Linux 服务器执行。默认 sing-box，可改为 Xray；不需要再填写面板地址、密钥或节点 ID。
4. 在服务器列表查看机器 ID、绑定节点和最后上报时间。需要变更时点击“编辑绑定”。

支持插件菜单的 XBoard 后台还会增加 **AriNode 服务器管理 → 服务器与部署命令 / 可绑定节点**。这里也能新增、编辑服务器，并直接显示命令。可视化页面额外提供节点勾选、搜索和复制按钮，无需修改面板前端文件。

节点协议、端口、订阅分组等仍在 XBoard 的节点管理中配置。其他服务器占用的节点不能直接抢占，需要先从原服务器解绑。XBoard 原生机器记录会显示在此页面；原有机器密钥不会因编辑而重置。

命令包含机器专属密钥，仅管理员接口返回。不要公开分享命令。页面不会通过 SSH 连接或自动执行命令。部署命令用于从零配置；已有 AriNode 配置会备份后替换，不会自动合并其他面板绑定。

已有 V2bX / xboard-node 请使用仓库文档中的迁移命令，保留原配置和迁回能力。DNS 多 IP 节点组仍使用原来的批量成员下发接口。

## 原有接口

- `POST /api/v1/arinode/provision`：为已有节点生成 AriNode JSON。
- `/api/v1/arinode/cluster/*`：节点组成员与聚合上报。
- `/api/v1/arinode/devices/alivelist`：原生节点在线 IP 信息。

[安装与页面说明](https://github.com/Foxtea267/AriNode/blob/main/docs/xboard-plugin.md) · [节点组说明](https://github.com/Foxtea267/AriNode/blob/main/docs/node-cluster.md)
